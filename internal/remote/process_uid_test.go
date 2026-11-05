package remote

import "testing"

func TestProcessEffectiveUIDDoesNotConfuseRealSavedFilesystemOrProcfsOwners(t *testing.T) {
	uid, err := processEffectiveUID([]byte("Name:\tfixture\nUid:\t123\t456\t789\t1011\nGid:\t1\t2\t3\t4\n"))
	if err != nil || uid != 456 {
		t.Fatal("effective process UID did not come from the kernel's effective column", uid, err)
	}
	root, err := processEffectiveUID([]byte("Uid:\t1000\t0\t1000\t1000\n"))
	if err != nil || root != 0 {
		t.Fatal("actual root effective UID was hidden", root, err)
	}
	for _, status := range []string{"Name:\tfixture\n", "Uid:\t1\t2\n", "Uid:\t1\t-1\t3\t4\n", "Uid:\t1\t4294967296\t3\t4\n"} {
		if _, err := processEffectiveUID([]byte(status)); err == nil {
			t.Fatal("missing/malformed kernel UID was silently accepted", status)
		}
	}
}
