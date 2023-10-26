package cmd

import (
	"encoding/json"
	"strings"

	goopts "github.com/sqlc-dev/sqlc/internal/codegen/golang/opts"
	"github.com/sqlc-dev/sqlc/internal/compiler"
	"github.com/sqlc-dev/sqlc/internal/config"
	"github.com/sqlc-dev/sqlc/internal/config/convert"
	"github.com/sqlc-dev/sqlc/internal/info"
	"github.com/sqlc-dev/sqlc/internal/plugin"
	"github.com/sqlc-dev/sqlc/internal/sql/catalog"
)

func pluginOverride(r *compiler.Result, o config.Override) *plugin.Override {
	var column string
	var table plugin.Identifier

	if o.Column != "" {
		colParts := strings.Split(o.Column, ".")
		switch len(colParts) {
		case 2:
			table.Schema = r.Catalog.DefaultSchema
			table.Name = colParts[0]
			column = colParts[1]
		case 3:
			table.Schema = colParts[0]
			table.Name = colParts[1]
			column = colParts[2]
		case 4:
			table.Catalog = colParts[0]
			table.Schema = colParts[1]
			table.Name = colParts[2]
			column = colParts[3]
		}
	}

	goTypeJSON, err := json.Marshal(pluginGoType(o))
	if err != nil {
		panic(err)
	}

	return &plugin.Override{
		CodeType:   goTypeJSON,
		DbType:     o.DBType,
		Nullable:   o.Nullable,
		Unsigned:   o.Unsigned,
		Column:     o.Column,
		ColumnName: column,
		Table:      &table,
	}
}

func pluginSettings(r *compiler.Result, cs config.CombinedSettings) *plugin.Settings {
	var overrides []*plugin.Override
	for _, o := range cs.Overrides {
		overrides = append(overrides, pluginOverride(r, o))
	}
	return &plugin.Settings{
		Version:   cs.Global.Version,
		Engine:    string(cs.Package.Engine),
		Schema:    []string(cs.Package.Schema),
		Queries:   []string(cs.Package.Queries),
		Overrides: overrides,
		Rename:    cs.Rename,
		Codegen:   pluginCodegen(cs, cs.Codegen),
	}
}

func pluginCodegen(cs config.CombinedSettings, s config.Codegen) *plugin.Codegen {
	opts, err := convert.YAMLtoJSON(s.Options)
	if err != nil {
		panic(err)
	}
	cg := &plugin.Codegen{
		Out:     s.Out,
		Plugin:  s.Plugin,
		Options: opts,
	}
	for _, p := range cs.Global.Plugins {
		if p.Name == s.Plugin {
			cg.Env = p.Env
			cg.Process = pluginProcess(p)
			cg.Wasm = pluginWASM(p)
			return cg
		}
	}
	return cg
}

func pluginProcess(p config.Plugin) *plugin.Codegen_Process {
	if p.Process != nil {
		return &plugin.Codegen_Process{
			Cmd: p.Process.Cmd,
		}
	}
	return nil
}

func pluginWASM(p config.Plugin) *plugin.Codegen_WASM {
	if p.WASM != nil {
		return &plugin.Codegen_WASM{
			Url:    p.WASM.URL,
			Sha256: p.WASM.SHA256,
		}
	}
	return nil
}

func pluginGoType(o config.Override) *goopts.ParsedGoType {
	// Note that there is a slight mismatch between this and the
	// proto api. The GoType on the override is the unparsed type,
	// which could be a qualified path or an object, as per
	// https://docs.sqlc.dev/en/v1.18.0/reference/config.html#type-overriding
	return &goopts.ParsedGoType{
		ImportPath: o.GoImportPath,
		Package:    o.GoPackage,
		TypeName:   o.GoTypeName,
		BasicType:  o.GoBasicType,
		StructTags: o.GoStructTags,
	}
}

func pluginCatalog(c *catalog.Catalog) *plugin.Catalog {
	var schemas []*plugin.Schema
	for _, s := range c.Schemas {
		var enums []*plugin.Enum
		var cts []*plugin.CompositeType
		for _, typ := range s.Types {
			switch typ := typ.(type) {
			case *catalog.Enum:
				enums = append(enums, &plugin.Enum{
					Name:    typ.Name,
					Comment: typ.Comment,
					Vals:    typ.Vals,
				})
			case *catalog.CompositeType:
				cts = append(cts, &plugin.CompositeType{
					Name:    typ.Name,
					Comment: typ.Comment,
				})
			}
		}
		var tables []*plugin.Table
		for _, t := range s.Tables {
			var columns []*plugin.Column
			for _, c := range t.Columns {
				l := -1
				if c.Length != nil {
					l = *c.Length
				}
				columns = append(columns, &plugin.Column{
					Name: c.Name,
					Type: &plugin.Identifier{
						Catalog: c.Type.Catalog,
						Schema:  c.Type.Schema,
						Name:    c.Type.Name,
					},
					Comment:   c.Comment,
					NotNull:   c.IsNotNull,
					Unsigned:  c.IsUnsigned,
					IsArray:   c.IsArray,
					ArrayDims: int32(c.ArrayDims),
					Length:    int32(l),
					Table: &plugin.Identifier{
						Catalog: t.Rel.Catalog,
						Schema:  t.Rel.Schema,
						Name:    t.Rel.Name,
					},
				})
			}
			tables = append(tables, &plugin.Table{
				Rel: &plugin.Identifier{
					Catalog: t.Rel.Catalog,
					Schema:  t.Rel.Schema,
					Name:    t.Rel.Name,
				},
				Columns: columns,
				Comment: t.Comment,
			})
		}
		schemas = append(schemas, &plugin.Schema{
			Comment:        s.Comment,
			Name:           s.Name,
			Tables:         tables,
			Enums:          enums,
			CompositeTypes: cts,
		})
	}
	return &plugin.Catalog{
		Name:          c.Name,
		DefaultSchema: c.DefaultSchema,
		Comment:       c.Comment,
		Schemas:       schemas,
	}
}

func pluginQueries(r *compiler.Result) []*plugin.Query {
	var out []*plugin.Query
	for _, q := range r.Queries {
		var params []*plugin.Parameter
		var columns []*plugin.Column
		for _, c := range q.Columns {
			columns = append(columns, pluginQueryColumn(c))
		}
		for _, p := range q.Params {
			params = append(params, pluginQueryParam(p))
		}
		var iit *plugin.Identifier
		if q.InsertIntoTable != nil {
			iit = &plugin.Identifier{
				Catalog: q.InsertIntoTable.Catalog,
				Schema:  q.InsertIntoTable.Schema,
				Name:    q.InsertIntoTable.Name,
			}
		}
		out = append(out, &plugin.Query{
			Name:            q.Metadata.Name,
			Cmd:             q.Metadata.Cmd,
			Text:            q.SQL,
			Comments:        q.Metadata.Comments,
			Columns:         columns,
			Params:          params,
			Filename:        q.Metadata.Filename,
			InsertIntoTable: iit,
		})
	}
	return out
}

func pluginQueryColumn(c *compiler.Column) *plugin.Column {
	l := -1
	if c.Length != nil {
		l = *c.Length
	}
	out := &plugin.Column{
		Name:         c.Name,
		OriginalName: c.OriginalName,
		Comment:      c.Comment,
		NotNull:      c.NotNull,
		Unsigned:     c.Unsigned,
		IsArray:      c.IsArray,
		ArrayDims:    int32(c.ArrayDims),
		Length:       int32(l),
		IsNamedParam: c.IsNamedParam,
		IsFuncCall:   c.IsFuncCall,
		IsSqlcSlice:  c.IsSqlcSlice,
	}

	if c.Type != nil {
		out.Type = &plugin.Identifier{
			Catalog: c.Type.Catalog,
			Schema:  c.Type.Schema,
			Name:    c.Type.Name,
		}
	} else {
		out.Type = &plugin.Identifier{
			Name: c.DataType,
		}
	}

	if c.Table != nil {
		out.Table = &plugin.Identifier{
			Catalog: c.Table.Catalog,
			Schema:  c.Table.Schema,
			Name:    c.Table.Name,
		}
	}

	if c.EmbedTable != nil {
		out.EmbedTable = &plugin.Identifier{
			Catalog: c.EmbedTable.Catalog,
			Schema:  c.EmbedTable.Schema,
			Name:    c.EmbedTable.Name,
		}
	}

	return out
}

func pluginQueryParam(p compiler.Parameter) *plugin.Parameter {
	return &plugin.Parameter{
		Number: int32(p.Number),
		Column: pluginQueryColumn(p.Column),
	}
}

func codeGenRequest(r *compiler.Result, settings config.CombinedSettings) *plugin.CodeGenRequest {
	return &plugin.CodeGenRequest{
		Settings:    pluginSettings(r, settings),
		Catalog:     pluginCatalog(r.Catalog),
		Queries:     pluginQueries(r),
		SqlcVersion: info.Version,
	}
}
