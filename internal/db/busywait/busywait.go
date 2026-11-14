package busywait

/*
#include <sqlite3.h>

typedef struct {
	sqlite3 *db;
	int ms;
	int slept;
} atrium_busy_state;

static int atrium_busy_wait(void *arg, int n) {
	atrium_busy_state *s = arg;
	if (n == 0) {
		s->slept = 0;
	}
	if (sqlite3_is_interrupted(s->db) || s->slept >= s->ms) {
		return 0;
	}
	int d = n < 10 ? n + 1 : 10;
	if (d > s->ms - s->slept) {
		d = s->ms - s->slept;
	}
	sqlite3_sleep(d);
	s->slept += d;
	return 1;
}

static void atrium_busy_timeout(sqlite3_context *ctx, int argc, sqlite3_value **argv) {
	atrium_busy_state *s = sqlite3_user_data(ctx);
	s->ms = sqlite3_value_int(argv[0]);
	if (s->ms > 0) {
		sqlite3_busy_handler(s->db, atrium_busy_wait, s);
	} else {
		sqlite3_busy_handler(s->db, 0, 0);
	}
}

static int atrium_busy_init(sqlite3 *db, char **err, const sqlite3_api_routines *api) {
	atrium_busy_state *s = sqlite3_malloc(sizeof *s);
	if (s == 0) {
		return SQLITE_NOMEM;
	}
	s->db = db;
	s->ms = 0;
	s->slept = 0;
	return sqlite3_create_function_v2(db, "atrium_busy_timeout", 1, SQLITE_UTF8 | SQLITE_DIRECTONLY, s, atrium_busy_timeout, 0, 0, sqlite3_free);
}

static void atrium_busy_register(void) {
	sqlite3_auto_extension((void (*)(void))atrium_busy_init);
}
*/
import "C"

func Register() { C.atrium_busy_register() }
