package cache

// SetCloneFunc swaps the git clone used by Resolve and returns a restore func.
func SetCloneFunc(fn func(ref *PackageRef, dir string) error) func() {
	old := cloneFn
	cloneFn = fn
	return func() { cloneFn = old }
}

// CloneFailureReason exposes the git output classifier to tests.
func CloneFailureReason(ref *PackageRef, dir, out string, err error) string {
	return cloneFailureReason(ref, dir, out, err)
}
