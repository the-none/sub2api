package schema

import (
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// UsageAlertWebhook stores outbound webhook endpoints for usage alerts.
type UsageAlertWebhook struct {
	ent.Schema
}

func (UsageAlertWebhook) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "usage_alert_webhooks"},
	}
}

func (UsageAlertWebhook) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
		mixins.SoftDeleteMixin{},
	}
}

func (UsageAlertWebhook) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").
			NotEmpty().
			MaxLen(100),
		field.String("url").
			NotEmpty().
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.Bool("enabled").
			Default(true),
		field.Int("retry_count").
			Default(2).
			Range(0, 10),
	}
}

func (UsageAlertWebhook) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("bindings", UsageAlertBinding.Type).
			Ref("webhook"),
	}
}

func (UsageAlertWebhook) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("enabled"),
		index.Fields("name"),
	}
}
