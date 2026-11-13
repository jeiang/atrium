package settings

func ClassOf(key string) (ImportClass, bool) {
	entry, ok := ImportEntryFor(key)
	if !ok {
		return "", false
	}
	return entry.Class, true
}

func CutoverEntry(key string) (ImportEntry, bool) {
	entry, ok := ImportEntryFor(key)
	if !ok || !entry.Runtime {
		return ImportEntry{}, false
	}
	return entry, true
}

func CutoverDestination(entry ImportEntry) string {
	return Destination(entry)
}

func HostOwnedKey(key string) bool {
	entry, ok := ImportEntryFor(key)
	return ok && entry.Class == ImportHost
}

func DBBacked(key string) bool {
	class, ok := ClassOf(key)
	return !ok || class == ImportOperator
}

func hostOwned(key string) error {
	entry, _ := ImportEntryFor(key)
	return HostOwned{Key: key, Destination: entry.Target}
}
