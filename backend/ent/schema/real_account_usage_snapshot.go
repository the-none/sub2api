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

// RealAccountUsageSnapshot stores the latest normalized usage snapshot for a
// real upstream account.
type RealAccountUsageSnapshot struct {
	ent.Schema
}

func (RealAccountUsageSnapshot) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "real_account_usage_snapshots"},
	}
}

func (RealAccountUsageSnapshot) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
	}
}

func (RealAccountUsageSnapshot) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("real_account_id"),
		field.String("platform").
			NotEmpty().
			MaxLen(50),
		field.String("source").
			NotEmpty().
			MaxLen(64),
		field.JSON("snapshot_json", map[string]any{}).
			Default(func() map[string]any { return map[string]any{} }).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		field.Time("sampled_at").
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (RealAccountUsageSnapshot) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("real_account", RealAccount.Type).
			Field("real_account_id").
			Unique().
			Required().
			Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}

func (RealAccountUsageSnapshot) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("real_account_id").Unique(),
		index.Fields("platform"),
		index.Fields("sampled_at"),
	}
}
