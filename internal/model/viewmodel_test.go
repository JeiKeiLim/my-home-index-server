package model_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/JeiKeiLim/my-home-index-server/internal/inspector"
	"github.com/JeiKeiLim/my-home-index-server/internal/model"
	"github.com/JeiKeiLim/my-home-index-server/internal/scanner"
)

type fakeInspector struct {
	infos    map[int]*inspector.ProcInfo
	errs     map[int]error
	inFlight int32
	maxSeen  int32
	delay    time.Duration
}

func (f *fakeInspector) Inspect(ctx context.Context, pid int) (*inspector.ProcInfo, error) {
	cur := atomic.AddInt32(&f.inFlight, 1)
	defer atomic.AddInt32(&f.inFlight, -1)
	for {
		old := atomic.LoadInt32(&f.maxSeen)
		if cur <= old || atomic.CompareAndSwapInt32(&f.maxSeen, old, cur) {
			break
		}
	}
	if f.delay > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(f.delay):
		}
	}
	if err, ok := f.errs[pid]; ok {
		return nil, err
	}
	if info, ok := f.infos[pid]; ok {
		return info, nil
	}
	return nil, inspector.ErrNotFound
}

type fakeStore struct {
	labels map[string]string // key: cwd
}

func (f *fakeStore) Label(cwd string, _ []string) (string, error) {
	if f == nil {
		return "", nil
	}
	return f.labels[cwd], nil
}

func TestBuildViewModelsAssemblesRows(t *testing.T) {
	now := time.Now()
	insp := &fakeInspector{
		infos: map[int]*inspector.ProcInfo{
			101: {PID: 101, Command: []string{"node", "server.js"}, Cwd: "/srv/api", StartTime: now.Add(-3 * time.Hour)},
			102: {PID: 102, Command: []string{"python", "-m", "http.server"}, Cwd: "/tmp", StartTime: now.Add(-30 * time.Second)},
		},
	}
	st := &fakeStore{labels: map[string]string{"/srv/api": "api-prod"}}
	listeners := []scanner.Listener{
		{PID: 101, Port: 40102, Protocol: "tcp", Addrs: []string{"0.0.0.0:40102"}, Source: "libproc"},
		{PID: 102, Port: 40050, Protocol: "tcp", Addrs: []string{"127.0.0.1:40050"}, Source: "libproc"},
	}
	vms := model.BuildViewModels(context.Background(), listeners, insp, st, 999)
	require.Len(t, vms, 2)
	// Sorted by port ascending.
	require.Equal(t, 40050, vms[0].Port)
	require.Equal(t, 40102, vms[1].Port)
	require.Equal(t, "api-prod", vms[1].Label)
	require.Equal(t, "node server.js", vms[1].Cmd)
	require.Equal(t, "/srv/api", vms[1].Cwd)
	require.GreaterOrEqual(t, vms[1].UptimeS, int64(60*60*2))
	require.True(t, vms[1].Alive)
	require.False(t, vms[1].Remembered)
	require.Equal(t, "external", vms[1].Source)
}

func TestBuildViewModelsExcludesSelfPID(t *testing.T) {
	insp := &fakeInspector{infos: map[int]*inspector.ProcInfo{
		101: {PID: 101, Command: []string{"x"}, Cwd: "/", StartTime: time.Now()},
	}}
	listeners := []scanner.Listener{
		{PID: 101, Port: 40100},
		{PID: 999, Port: 40000},
	}
	vms := model.BuildViewModels(context.Background(), listeners, insp, nil, 999)
	require.Len(t, vms, 1)
	require.Equal(t, 101, vms[0].PID)
}

func TestBuildViewModelsHandlesInspectFailures(t *testing.T) {
	insp := &fakeInspector{
		infos: map[int]*inspector.ProcInfo{},
		errs:  map[int]error{101: errors.New("boom")},
	}
	listeners := []scanner.Listener{
		{PID: 101, Port: 40190, Addrs: []string{"::1:40190"}},
	}
	vms := model.BuildViewModels(context.Background(), listeners, insp, nil, 0)
	require.Len(t, vms, 1)
	require.Equal(t, 40190, vms[0].Port)
	require.Equal(t, 101, vms[0].PID)
	require.True(t, vms[0].Alive)
	require.Empty(t, vms[0].Cmd)
}

func TestBuildViewModelsBoundedConcurrency(t *testing.T) {
	insp := &fakeInspector{
		infos: map[int]*inspector.ProcInfo{},
		delay: 20 * time.Millisecond,
	}
	for i := 1; i <= 32; i++ {
		insp.infos[i] = &inspector.ProcInfo{PID: i, StartTime: time.Now(), Command: []string{"x"}}
	}
	listeners := make([]scanner.Listener, 32)
	for i := range listeners {
		listeners[i] = scanner.Listener{PID: i + 1, Port: 40000 + i}
	}
	vms := model.BuildViewModels(context.Background(), listeners, insp, nil, 0)
	require.Len(t, vms, 32)
	require.LessOrEqual(t, int(insp.maxSeen), model.MaxParallelInspect, "concurrency must not exceed semaphore size")
}

func TestFormatUptime(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"},
		{45 * time.Second, "45s"},
		{2 * time.Minute, "2m"},
		{2*time.Hour + 18*time.Minute, "2h 18m"},
		{27 * time.Hour, "1d 3h"},
	}
	for _, c := range cases {
		require.Equalf(t, c.want, model.FormatUptime(c.d), "duration %s", c.d)
	}
}
