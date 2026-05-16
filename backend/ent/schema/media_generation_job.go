package schema

import (
	"encoding/json"
	"fmt"

	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type MediaGenerationJob struct {
	ent.Schema
}

func (MediaGenerationJob) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "media_generation_jobs"},
	}
}

func (MediaGenerationJob) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
	}
}

func (MediaGenerationJob) Fields() []ent.Field {
	return []ent.Field{
		field.String("public_id").MaxLen(80).Unique(),
		field.String("kind").MaxLen(40),
		field.String("provider").MaxLen(40),
		field.String("platform").MaxLen(40),
		field.String("status").MaxLen(30).Validate(validateMediaGenerationJobStatus),
		field.String("upstream_status").MaxLen(80).Optional().Nillable(),
		field.String("upstream_task_id").MaxLen(160).Optional().Nillable(),
		field.String("upstream_request_id").MaxLen(160).Optional().Nillable(),
		field.Int64("user_id"),
		field.Int64("api_key_id"),
		field.Int64("group_id").Optional().Nillable(),
		field.Int64("account_id"),
		field.String("model").MaxLen(160),
		field.JSON("request_json", json.RawMessage{}).Optional(),
		field.JSON("upstream_response_json", json.RawMessage{}).Optional(),
		field.String("result_url").Optional().Nillable(),
		field.String("result_content_type").MaxLen(120).Optional().Nillable(),
		field.Time("expires_at").Optional().Nillable(),
		field.String("audio_voice").MaxLen(160).Optional().Nillable(),
		field.String("audio_format").MaxLen(80).Optional().Nillable(),
		field.Int("audio_character_count").Default(0),
		field.Int("video_duration_seconds").Default(0),
		field.String("video_resolution").MaxLen(40).Optional().Nillable(),
		field.String("video_ratio").MaxLen(40).Optional().Nillable(),
		field.Int("video_count").Default(0),
		field.String("error_code").MaxLen(120).Optional().Nillable(),
		field.String("error_message").Optional().Nillable(),
		field.Time("usage_recorded_at").Optional().Nillable(),
		field.Time("submitted_at").Optional().Nillable(),
		field.Time("completed_at").Optional().Nillable(),
	}
}

func (MediaGenerationJob) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("public_id").Unique(),
		index.Fields("user_id", "created_at"),
		index.Fields("api_key_id", "created_at"),
		index.Fields("account_id", "status"),
		index.Fields("provider", "upstream_task_id"),
	}
}

func validateMediaGenerationJobStatus(status string) error {
	switch status {
	case "queued", "running", "succeeded", "failed", "canceled", "unknown":
		return nil
	default:
		return fmt.Errorf("invalid media generation job status: %s", status)
	}
}
