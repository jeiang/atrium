package projects

func JoinClause(projectAlias, recordAlias string) string {
	return " LEFT JOIN projects " + projectAlias + " ON " + projectAlias + ".id = " + recordAlias + ".project_id"
}

func PrivateIDsSQL() string { return "SELECT id FROM projects WHERE private = 1" }
