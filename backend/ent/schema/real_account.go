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

// RealAccount represents the real upstream subscription identity behind one or
// more schedulable Sub2API accounts.
type RealAccount struct {
	ent.Schema
}

func (RealAccount) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "real_accounts"},
	}
}

func (RealAccount) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
		mixins.SoftDeleteMixin{},
	}
}

func (RealAccount) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").
			NotEmpty().
			MaxLen(100),
		field.String("platform").
			NotEmpty().
			MaxLen(50),
		field.String("identifier").
			Optional().
			Nillable().
			MaxLen(255),
		field.String("notes").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}),
	}
}

func (RealAccount) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("accounts", Account.Type).
			Ref("real_account"),
		edge.From("usage_snapshot", RealAccountUsageSnapshot.Type).
			Ref("real_account"),
		edge.From("webhook_bindings", UsageAlertBinding.Type).
			Ref("real_account"),
		edge.From("alert_rules", UsageAlertRule.Type).
			Ref("real_account"),
		edge.From("alert_states", UsageAlertState.Type).
			Ref("real_account"),
	}
}

func (RealAccount) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("platform"),
		index.Fields("name"),
	}
}
