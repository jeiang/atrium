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
	switch entry.Class {
	case ImportTuning:
		return "the compiled constant " + entry.Target
	case ImportHost:
		return "host configuration " + entry.Target
	default:
		if entry.Action == ImportRename {
			return "the connection field " + entry.Target
		}
		return "the operator setting " + entry.Module + "." + entry.Key + " stored in the database"
	}
}

func HostKey(key string) bool {
	entry, ok := ImportEntryFor(key)
	return ok && entry.Class == ImportHost
}
