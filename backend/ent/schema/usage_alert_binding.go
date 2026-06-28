package schema

import (
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// UsageAlertBinding binds a real account to a webhook endpoint.
type UsageAlertBinding struct {
	ent.Schema
}

func (UsageAlertBinding) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "usage_alert_bindings"},
	}
}

func (UsageAlertBinding) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
	}
}

func (UsageAlertBinding) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("real_account_id"),
		field.Int64("webhook_id"),
		field.Bool("enabled").Default(true),
	}
}

func (UsageAlertBinding) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("real_account", RealAccount.Type).
			Field("real_account_id").
			Unique().
			Required().
			Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("webhook", UsageAlertWebhook.Type).
			Field("webhook_id").
			Unique().
			Required().
			Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}

func (UsageAlertBinding) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("real_account_id", "webhook_id").Unique(),
		index.Fields("webhook_id"),
	}
}
