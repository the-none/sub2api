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

// UsageAlertRule defines an editable threshold rule for normalized account
// usage snapshots.
type UsageAlertRule struct {
	ent.Schema
}

func (UsageAlertRule) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "usage_alert_rules"},
	}
}

func (UsageAlertRule) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
		mixins.SoftDeleteMixin{},
	}
}

func (UsageAlertRule) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").
			NotEmpty().
			MaxLen(100),
		field.String("platform").
			Default("all").
			MaxLen(50),
		field.String("window").
			NotEmpty().
			MaxLen(32),
		field.String("metric").
			NotEmpty().
			MaxLen(32),
		field.String("operator").
			NotEmpty().
			MaxLen(4),
		field.Float("threshold").
			SchemaType(map[string]string{dialect.Postgres: "decimal(10,4)"}),
		field.Float("min_reset_after_hours").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "decimal(10,4)"}),
		field.Int("cooldown_minutes").
			Default(240).
			NonNegative(),
		field.Bool("enabled").
			Default(true),
	}
}

func (UsageAlertRule) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("states", UsageAlertState.Type).
			Ref("rule"),
	}
}

func (UsageAlertRule) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("enabled", "platform"),
		index.Fields("window"),
	}
}
