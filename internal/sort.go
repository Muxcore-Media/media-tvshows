package internal

// resolveSeriesSortColumn maps API sort keys to real SQLite column names.
// Unknown keys return "" so callers fall back to a safe default.
func resolveSeriesSortColumn(sortBy string) string {
	switch sortBy {
	case "name", "sort_name":
		return "name"
	case "year":
		return "year"
	case "rating":
		return "vote_average"
	case "status":
		return "status"
	case "network":
		return "network"
	case "added_at":
		return "created_at"
	case "updated_at":
		return "updated_at"
	default:
		return ""
	}
}
