package explain

import "testing"

func TestKernelABI(t *testing.T) {
	cases := []struct {
		pkg string
		abi string
		ok  bool
	}{
		{"linux-image-7.0.0-28-generic", "7.0.0-28-generic", true},
		{"linux-modules-7.0.0-14-generic", "7.0.0-14-generic", true},
		{"linux-modules-extra-6.8.0-40-generic", "6.8.0-40-generic", true},
		{"linux-headers-7.0.0-28-generic", "7.0.0-28-generic", true},
		{"linux-main-modules-zfs-7.0.0-28-generic", "7.0.0-28-generic", true},
		{"linux-image-generic", "", false},
		{"linux-headers-generic", "", false},
		{"openssh-server", "", false},
	}
	for _, c := range cases {
		abi, ok := kernelABI(c.pkg)
		if ok != c.ok || abi != c.abi {
			t.Errorf("kernelABI(%q) = %q, %v; want %q, %v", c.pkg, abi, ok, c.abi, c.ok)
		}
	}
}
