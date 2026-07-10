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

// UsageAlertState stores per-rule trigger state to avoid repeated alerts.
type UsageAlertState struct {
	ent.Schema
}

func (UsageAlertState) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "usage_alert_states"},
	}
}

func (UsageAlertState) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
	}
}

func (UsageAlertState) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("real_account_id"),
		field.Int64("rule_id"),
		field.String("quota_dimension").
			Default("global").
			MaxLen(32),
		field.String("window").
			NotEmpty().
			MaxLen(32),
		field.String("last_status").
			Default("normal").
			MaxLen(20),
		field.Time("last_triggered_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Float("last_value").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "decimal(10,4)"}),
		field.Time("last_reset_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (UsageAlertState) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("real_account", RealAccount.Type).
			Field("real_account_id").
			Unique().
			Required().
			Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.To("rule", UsageAlertRule.Type).
			Field("rule_id").
			Unique().
			Required().
			Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}

func (UsageAlertState) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("real_account_id", "rule_id", "quota_dimension", "window").Unique(),
		index.Fields("rule_id"),
	}
}
