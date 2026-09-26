package arch

const (
	X8664 = "x86_64"
	ARM64 = "arm64"
)

func Architecture(declared string) string {
	if declared == "" {
		return X8664
	}
	return declared
}

func GoArch(declared string) (string, bool) {
	switch Architecture(declared) {
	case X8664:
		return "amd64", true
	case ARM64:
		return "arm64", true
	}
	return "", false
}

const (
	NodePackageOS   = "linux"
	NodePackageLibc = "glibc"
)

func NodePackageCPU(declared string) (string, bool) {
	switch Architecture(declared) {
	case X8664:
		return "x64", true
	case ARM64:
		return "arm64", true
	}
	return "", false
}

func RustTarget(declared string) (string, bool) {
	switch Architecture(declared) {
	case X8664:
		return "x86_64-unknown-linux-musl", true
	case ARM64:
		return "aarch64-unknown-linux-musl", true
	}
	return "", false
}

const PythonVersion = "3.13"

func PythonPlatformTag(declared string) (string, bool) {
	switch Architecture(declared) {
	case X8664:
		return "manylinux2014_x86_64", true
	case ARM64:
		return "manylinux2014_aarch64", true
	}
	return "", false
}

const (
	elfMachineX8664 = 0x3E
	elfMachineARM64 = 0xB7
)

func ELFMachine(declared string) (uint16, bool) {
	switch Architecture(declared) {
	case X8664:
		return elfMachineX8664, true
	case ARM64:
		return elfMachineARM64, true
	}
	return 0, false
}

func OfELFMachine(machine uint16) (string, bool) {
	switch machine {
	case elfMachineX8664:
		return X8664, true
	case elfMachineARM64:
		return ARM64, true
	}
	return "", false
}
