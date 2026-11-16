package settings

func ClassOf(key string) (Class, bool) {
	entry, ok := EntryFor(key)
	if !ok {
		return "", false
	}
	return entry.Class, true
}

func CutoverEntry(key string) (Entry, bool) {
	entry, ok := EntryFor(key)
	if !ok || !entry.Runtime {
		return Entry{}, false
	}
	return entry, true
}

func CutoverDestination(entry Entry) string {
	return Destination(entry)
}

func HostOwnedKey(key string) bool {
	entry, ok := EntryFor(key)
	return ok && entry.Class == ClassHost
}

func DBBacked(key string) bool {
	class, ok := ClassOf(key)
	return !ok || class == ClassOperator
}

func hostOwned(key string) error {
	entry, _ := EntryFor(key)
	return HostOwned{Key: key, Destination: entry.Target}
}
