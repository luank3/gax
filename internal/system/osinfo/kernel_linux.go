package osinfo

import "syscall"

func kernelIdentity() (system, release string, ok bool) {
	var identity syscall.Utsname
	if err := syscall.Uname(&identity); err != nil {
		return "", "", false
	}
	return cString(identity.Sysname[:]), cString(identity.Release[:]), true
}

func cString(value []int8) string {
	bytes := make([]byte, 0, len(value))
	for _, character := range value {
		if character == 0 {
			break
		}
		bytes = append(bytes, byte(character))
	}
	return string(bytes)
}
