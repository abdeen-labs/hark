package mcp

import (
	"maps"
	"slices"
)

// Output schemas describe the API's JSON without decoding or reshaping it.
// Keep these in step with the response DTOs in internal/httpapi; the contract
// tests there validate those DTOs against the schemas published by tools/list.
type outputSchema map[string]any

func outputField(kind, description string) outputSchema {
	return outputSchema{"type": kind, "description": description}
}

func nullableOutput(kind, description string) outputSchema {
	return outputSchema{"type": []string{kind, "null"}, "description": description}
}

// Every listed property is required unless explicitly named optional. Leave
// additional properties allowed so additive API changes remain compatible.
func outputObject(properties map[string]outputSchema, optional ...string) outputSchema {
	required := slices.Sorted(maps.Keys(properties))
	required = slices.DeleteFunc(required, func(key string) bool { return slices.Contains(optional, key) })
	return outputSchema{"type": "object", "properties": properties, "required": required}
}

func outputArray(item outputSchema) outputSchema {
	return outputSchema{"type": "array", "items": item}
}

func outputPage(key string, item outputSchema) outputSchema {
	return outputObject(map[string]outputSchema{
		key:           outputArray(item),
		"next_cursor": nullableOutput("string", "Pass as cursor for the next page; null means no more results."),
	})
}

func toolOutputSchemas() map[string]outputSchema {
	str := outputField("string", "")
	nullStr := nullableOutput("string", "")
	integer := outputField("integer", "")
	boolean := outputField("boolean", "")
	timestamp := outputField("string", "RFC 3339 UTC timestamp.")
	nullTime := nullableOutput("string", "RFC 3339 UTC timestamp, or null if the event has not occurred.")
	replayed := outputField("boolean", "True when an idempotency key returned an earlier result.")
	message := nullableOutput("string", "Explains a fallback or delivery issue; null when there is none.")
	accepted := outputField("integer", "Devices whose push was accepted by APNs; not confirmation it was displayed.")

	notification := outputObject(map[string]outputSchema{
		"id": str, "title": str, "body": str, "image_url": nullStr, "url": nullStr,
		"pass_url": nullStr, "priority": str, "accepted_count": accepted, "created_at": timestamp,
	})
	question := outputObject(map[string]outputSchema{
		"id": str, "title": str, "prompt": str,
		"kind":         outputField("string", "approval, yes_no, or reply."),
		"presentation": outputField("string", "notification or live_activity; reflects the actual presentation after any fallback."),
		"status":       outputField("string", "pending, approved, denied, yes, no, replied, canceled, or expired. Only pending can still be answered."),
		"choices":      outputArray(str),
		"response":     nullableOutput("string", "The answer: approve/deny, yes/no, or free text. Null until answered."),
		"url":          nullStr, "image_url": nullStr, "action_digest": str,
		"primary_label": nullStr, "secondary_label": nullStr, "correlation_id": nullStr,
		"accepted_count": accepted, "responding_device_id": nullStr,
		"expires_at": outputField("string", "RFC 3339 UTC deadline; stop waiting for an answer after this time."),
		"created_at": timestamp, "responded_at": nullTime, "canceled_at": nullTime,
	})
	state := outputObject(map[string]outputSchema{
		"schema_version": integer, "activity_id": str, "updated_at": timestamp,
		"title": str, "status": str, "detail": str,
		"progress": outputField("number", "Completion from 0.0 to 1.0; omitted when not shown."),
		"symbol":   str, "privacy_mode": str, "accent_color": str, "style": str,
		"interaction": outputObject(map[string]outputSchema{
			"id": str, "kind": str, "prompt": str, "primary_label": str,
			"secondary_label": str, "primary_action": str, "secondary_action": str, "state": str,
		}),
	}, "detail", "progress", "interaction")
	activity := outputObject(map[string]outputSchema{
		"id": str, "key": nullStr,
		"status":   outputField("string", "starting, active, partial, failed, ended, or expired."),
		"sequence": outputField("integer", "Current version; pass as if_sequence for a conditional update or end."),
		"state":    state, "accepted_count": accepted,
		"failed_count": outputField("integer", "Devices that failed the most recent operation."),
		"expires_at":   timestamp, "stale_at": nullTime, "created_at": timestamp,
		"updated_at": timestamp, "ended_at": nullTime,
	})
	withSource := func(record outputSchema) outputSchema {
		properties := maps.Clone(record["properties"].(map[string]outputSchema))
		properties["source_name"] = str
		properties["source_image_url"] = nullStr
		return outputObject(properties)
	}
	device := outputObject(map[string]outputSchema{
		"id": str, "name": nullStr, "platform": str, "active": boolean,
		"interaction_schema_version":        nullableOutput("integer", "Supported question schema version; null if unsupported."),
		"live_activity_interaction_version": nullableOutput("integer", "Supported Lock Screen question version; null if unsupported."),
		"live_activity_capable":             boolean, "push_to_start_environment": nullStr,
		"push_to_start_updated_at": nullTime, "created_at": timestamp, "last_seen_at": timestamp,
	})
	service := outputObject(map[string]outputSchema{
		"id": str, "title": str, "image_url": nullStr, "url": nullStr, "priority": str,
		"webhook_url": {"type": "null", "description": "Webhook credentials are hidden from API tokens."},
		"created_at":  timestamp, "updated_at": timestamp,
	})
	event := outputObject(map[string]outputSchema{
		"id": str, "service_id": str, "service_name": str, "title": str, "body": str,
		"image_url": nullStr, "url": nullStr, "pass_url": nullStr, "priority": str, "status": str,
		"delivered_count": accepted, "error": nullStr, "created_at": timestamp,
	})
	questionRead := outputObject(map[string]outputSchema{"interaction": question})
	activityRead := outputObject(map[string]outputSchema{"activity": activity})
	activityWrite := outputObject(map[string]outputSchema{
		"activity": activity, "accepted": accepted, "failed": integer,
		"replaced": outputField("integer", "Activities ended to make room; present only when replacement was requested."),
		"replayed": replayed, "message": message,
	}, "replaced")
	return map[string]outputSchema{
		"send_notification": outputObject(map[string]outputSchema{
			"notification": notification, "replayed": replayed, "message": message,
		}),
		"ask_question": outputObject(map[string]outputSchema{
			"interaction": question, "accepted": accepted,
			"activity_id": nullableOutput("string", "Live Activity presenting the question; null for a notification."),
			"replayed":    replayed, "message": message,
		}),
		"get_question": questionRead, "cancel_question": questionRead,
		"list_questions":      outputPage("interactions", withSource(question)),
		"start_live_activity": activityWrite, "update_live_activity": activityWrite,
		"end_live_activity": activityWrite, "get_live_activity": activityRead,
		"list_live_activities": outputPage("activities", withSource(activity)),
		"list_devices":         outputObject(map[string]outputSchema{"devices": outputArray(device)}),
		"list_services":        outputObject(map[string]outputSchema{"services": outputArray(service)}),
		"list_webhook_events":  outputPage("events", event),
		"search": outputObject(map[string]outputSchema{
			"results": outputArray(outputObject(map[string]outputSchema{
				"id": outputField("string", "kind:id to pass to fetch."), "title": str, "url": str,
			})),
		}),
		"fetch": outputObject(map[string]outputSchema{
			"id": str, "title": str, "url": str,
			"text": outputField("string", "The fetched record serialized as JSON."),
			"metadata": outputObject(map[string]outputSchema{
				"kind": outputField("string", "service, device, event, question, or activity."),
			}),
		}),
	}
}
