package cache

// SetCloneFunc swaps the git clone used by Resolve and returns a restore func.
func SetCloneFunc(fn func(ref *PackageRef, dir string) error) func() {
	old := cloneFn
	cloneFn = fn
	return func() { cloneFn = old }
}
