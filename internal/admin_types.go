package internal

import mediaadminv1 "github.com/Muxcore-Media/contracts-media-admin/gen/muxcore/media/admin/v1"

func artworkKindToProtoType(kind string) mediaadminv1.ArtworkType {
	switch kind {
	case "poster":
		return mediaadminv1.ArtworkType_ARTWORK_TYPE_POSTER
	case "backdrop", "background":
		return mediaadminv1.ArtworkType_ARTWORK_TYPE_BACKGROUND
	case "still":
		return mediaadminv1.ArtworkType_ARTWORK_TYPE_STILL
	default:
		return mediaadminv1.ArtworkType_ARTWORK_TYPE_UNSPECIFIED
	}
}

func artworkProtoTypeToKind(t mediaadminv1.ArtworkType) string {
	switch t {
	case mediaadminv1.ArtworkType_ARTWORK_TYPE_POSTER:
		return "poster"
	case mediaadminv1.ArtworkType_ARTWORK_TYPE_BACKGROUND:
		return "backdrop"
	case mediaadminv1.ArtworkType_ARTWORK_TYPE_STILL:
		return "still"
	default:
		return ""
	}
}

func historyEventTypeToDB(t mediaadminv1.HistoryEventType) string {
	switch t {
	case mediaadminv1.HistoryEventType_HISTORY_EVENT_TYPE_GRAB:
		return historyGrab
	case mediaadminv1.HistoryEventType_HISTORY_EVENT_TYPE_IMPORT:
		return historyImport
	case mediaadminv1.HistoryEventType_HISTORY_EVENT_TYPE_DELETE_ITEM:
		return historyDeleteItem
	case mediaadminv1.HistoryEventType_HISTORY_EVENT_TYPE_DELETE_FILE:
		return historyDeleteFile
	default:
		return ""
	}
}

func historyEventTypeFromDB(s string) mediaadminv1.HistoryEventType {
	switch s {
	case historyGrab:
		return mediaadminv1.HistoryEventType_HISTORY_EVENT_TYPE_GRAB
	case historyImport:
		return mediaadminv1.HistoryEventType_HISTORY_EVENT_TYPE_IMPORT
	case historyDeleteItem:
		return mediaadminv1.HistoryEventType_HISTORY_EVENT_TYPE_DELETE_ITEM
	case historyDeleteFile:
		return mediaadminv1.HistoryEventType_HISTORY_EVENT_TYPE_DELETE_FILE
	default:
		return mediaadminv1.HistoryEventType_HISTORY_EVENT_TYPE_UNSPECIFIED
	}
}

func resolveAdminSortColumn(sortBy mediaadminv1.SortField) string {
	switch sortBy {
	case mediaadminv1.SortField_SORT_FIELD_TITLE:
		return "name"
	case mediaadminv1.SortField_SORT_FIELD_YEAR:
		return "year"
	case mediaadminv1.SortField_SORT_FIELD_CREATED_AT:
		return "created_at"
	case mediaadminv1.SortField_SORT_FIELD_UPDATED_AT:
		return "updated_at"
	case mediaadminv1.SortField_SORT_FIELD_RATING:
		return "vote_average"
	default:
		return "name"
	}
}
