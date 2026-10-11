package app

import "github.com/luank3/gax/internal/provider/runtime"

func replaceProvider(current **runtime.Provider, next *runtime.Provider) {
	if current == nil {
		return
	}
	previous := *current
	if previous != nil && previous != next {
		previous.Close()
	}
	*current = next
}
