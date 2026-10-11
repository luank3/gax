//go:build cgo && (linux || darwin || freebsd || openbsd || netbsd || dragonfly)

package locale

/*
#include <langinfo.h>
#include <locale.h>
#include <string.h>

static const char *gax_locale_utf8_ctype(void) {
	static const char *const candidates[] = {"C.UTF-8", "C.utf8", "UTF-8", "en_US.UTF-8"};
	const char *selected = setlocale(LC_CTYPE, "");
	if (selected && strcmp(nl_langinfo(CODESET), "UTF-8") == 0)
		return "";
	for (size_t index = 0; index < sizeof(candidates) / sizeof(candidates[0]); index++) {
		selected = setlocale(LC_CTYPE, candidates[index]);
		if (selected && strcmp(nl_langinfo(CODESET), "UTF-8") == 0)
			return selected;
	}
	return NULL;
}
*/
import "C"

func nativeProcessLocale(environment []string) (State, bool) {
	selected := C.gax_locale_utf8_ctype()
	if selected == nil {
		return State{}, true
	}
	ctype := C.GoString(selected)
	if ctype == "" {
		return State{UTF8: true, ChildrenUTF8: true}, true
	}
	all, _ := environmentValue(environment, "LC_ALL")
	state := State{UTF8: true, ChildrenUTF8: all == "", ChildCType: ctype}
	return state, true
}
