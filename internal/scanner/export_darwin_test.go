//go:build darwin

package scanner

// ListAllPIDsForTest exposes listAllPIDs to external tests so the
// proc_listallpids return-value convention regression is guarded
// without reaching into C via the test binary.
func ListAllPIDsForTest() ([]int32, error) { return listAllPIDs() }
