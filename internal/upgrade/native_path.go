package upgrade

func CheckProtectedPath(path string, owner uint32) error {
	return protected(path, owner)
}
