// Copyright 2020 Google LLC. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/okkoye/gnostic/printer"
)

// patternNames hands out unique names for a given string.
type patternNames struct {
	prefix string
	values map[string]int
	last   int

	specialCase map[string]func(variable string) string
}

// SpecialCaseExpression returns true if the provided regex can be inlined as a faster
// expression.
func (p *patternNames) SpecialCaseExpression(value, variable string) (code string, ok bool) {
	fn, ok := p.specialCase[value]
	if !ok {
		return "", false
	}
	return fn(variable), ok
}

// VariableName returns the variable name for the given value.
func (p *patternNames) VariableName(value string) string {
	num, ok := p.values[value]
	if !ok {
		if p.values == nil {
			p.values = make(map[string]int)
		}
		num = p.last
		p.last++
		p.values[value] = num
	}
	return fmt.Sprintf("%s%d", p.prefix, num)
}

func (p *patternNames) Names() map[string]string {
	names := make(map[string]string)
	for value, num := range p.values {
		names[fmt.Sprintf("%s%d", p.prefix, num)] = value
	}
	return names
}

// GenerateCompiler generates the compiler code for a domain.
func (domain *Domain) GenerateCompiler(packageName string, license string, imports []string) string {
	code := &printer.Code{}
	code.Print(license)
	code.Print("// THIS FILE IS AUTOMATICALLY GENERATED.\n")

	// generate package declaration
	code.Print("package %s\n", packageName)

	code.Print("import (")
	for _, filename := range imports {
		code.Print("\"" + filename + "\"")
	}
	code.Print(")\n")

	// generate a simple Version() function
	code.Print("// Version returns the package name (and OpenAPI version).")
	code.Print("func Version() string {")
	code.Print("  return \"%s\"", packageName)
	code.Print("}\n")

	typeNames := domain.sortedTypeNames()

	regexPatterns := &patternNames{
		prefix: "pattern",
		specialCase: map[string]func(string) string{
			"^x-": func(variable string) string { return fmt.Sprintf("strings.HasPrefix(%s, \"x-\")", variable) },
			"^/":  func(variable string) string { return fmt.Sprintf("strings.HasPrefix(%s, \"/\")", variable) },
			"^":   func(_ string) string { return "true" },
		},
	}

	// generate NewX() constructor functions for each type
	for _, typeName := range typeNames {
		domain.generateConstructorForType(code, typeName, regexPatterns)
	}

	// generate ResolveReferences() methods for each type
	for _, typeName := range typeNames {
		domain.generateResolveReferencesMethodsForType(code, typeName)
	}

	// generate ToRawInfo() methods for each type
	for _, typeName := range typeNames {
		domain.generateToRawInfoMethodForType(code, typeName)
	}

	// generate precompiled regexps for use during parsing
	domain.generateConstantVariables(code, regexPatterns)

	return code.String()
}

func escapeSlashes(pattern string) string {
	return strings.Replace(pattern, "\\", "\\\\", -1)
}

var subpatternPattern = regexp.MustCompile("^.*(\\{.*\\}).*$")

func nameForPattern(regexPatterns *patternNames, pattern string) string {
	if !strings.HasPrefix(pattern, "^") {
		if matches := subpatternPattern.FindStringSubmatch(pattern); matches != nil {
			match := string(matches[1])
			pattern = strings.Replace(pattern, match, ".*", -1)
		}
	}
	return regexPatterns.VariableName(pattern)
}

func (domain *Domain) generateConstructorForType(code *printer.Code, typeName string, regexPatterns *patternNames) {
	code.Print("// New%s creates an object of type %s if possible, returning an error if not.", typeName, typeName)
	code.Print("func New%s(in *yaml.Node, context *compiler.Context) (*%s, error) {", typeName, typeName)
	code.Print("errors := make([]error, 0)")

	typeModel := domain.TypeModels[typeName]
	parentTypeName := typeName

	if typeModel.IsStringArray {
		code.Print("x := &TypeItem{}")
		code.Print("v1 := in")
		code.Print("switch v1.Kind {")
		code.Print("case yaml.ScalarNode:")
		code.Print("  x.Value = make([]string, 0)")
		code.Print("  x.Value = append(x.Value, v1.Value)")
		code.Print("case yaml.SequenceNode:")
		code.Print("  x.Value = make([]string, 0)")
		code.Print("  for _, v := range v1.Content {")
		code.Print("    value := v.Value")
		code.Print("    ok := v.Kind == yaml.ScalarNode")
		code.Print("    if ok {")
		code.Print("      x.Value = append(x.Value, value)")
		code.Print("    } else {")
		code.Print("      message := fmt.Sprintf(\"has unexpected value for string array element: %%+v (%%T)\", value, value)")
		code.Print("      errors = append(errors, compiler.NewError(context, message))")
		code.Print("    }")
		code.Print("  }")
		code.Print("default:")
		code.Print("  message := fmt.Sprintf(\"has unexpected value for string array: %%+v (%%T)\", in, in)")
		code.Print("  errors = append(errors, compiler.NewError(context, message))")
		code.Print("}")
	} else if typeModel.IsItemArray {
		if domain.Version == "v2" {
			code.Print("x := &ItemsItem{}")
			code.Print("m, ok := compiler.UnpackMap(in)")
			code.Print("if !ok {")
			code.Print("  message := fmt.Sprintf(\"has unexpected value for item array: %%+v (%%T)\", in, in)")
			code.Print("  errors = append(errors, compiler.NewError(context, message))")
			code.Print("} else {")
			code.Print("  x.Schema = make([]*Schema, 0)")
			code.Print("  y, err := NewSchema(m, compiler.NewContext(\"<array>\", m, context))")
			code.Print("  if err != nil {")
			code.Print("    return nil, err")
			code.Print("  }")
			code.Print("  x.Schema = append(x.Schema, y)")
			code.Print("}")
		} else if domain.Version == "v3" {
			code.Print("x := &ItemsItem{}")
			code.Print("m, ok := compiler.UnpackMap(in)")
			code.Print("if !ok {")
			code.Print("  message := fmt.Sprintf(\"has unexpected value for item array: %%+v (%%T)\", in, in)")
			code.Print("  errors = append(errors, compiler.NewError(context, message))")
			code.Print("} else {")
			code.Print("  x.SchemaOrReference = make([]*SchemaOrReference, 0)")
			code.Print("  y, err := NewSchemaOrReference(m, compiler.NewContext(\"<array>\", m, context))")
			code.Print("  if err != nil {")
			code.Print("    return nil, err")
			code.Print("  }")
			code.Print("  x.SchemaOrReference = append(x.SchemaOrReference, y)")
			code.Print("}")
		}
	} else if typeModel.IsBlob {
		code.Print("x := &Any{}")
		code.Print("bytes := compiler.Marshal(in)")
		code.Print("x.Yaml = string(bytes)")
	} else if typeModel.Name == "StringArray" {
		code.Print("x := &StringArray{}")
		code.Print("x.Value = make([]string, 0)")
		code.Print("for _, node := range in.Content {")
		code.Print("  s, _ := compiler.StringForScalarNode(node)")
		code.Print("  x.Value = append(x.Value, s)")
		code.Print("}")
	} else if typeModel.Name == "SpecificationExtension" {
		code.Print("	x := &SpecificationExtension{}")
		code.Print("	matched := false")
		code.Print("	switch in.Tag {")
		code.Print("    case \"!!bool\":")
		code.Print("        var v bool")
		code.Print("        v, matched = compiler.BoolForScalarNode(in)")
		code.Print("		x.Oneof = &SpecificationExtension_Boolean{Boolean: v}")
		code.Print("    case \"!!str\":")
		code.Print("        var v string")
		code.Print("        v, matched = compiler.StringForScalarNode(in)")
		code.Print("		x.Oneof = &SpecificationExtension_String_{String_: v}")
		code.Print("    case \"!!float\":")
		code.Print("        var v float64")
		code.Print("        v, matched = compiler.FloatForScalarNode(in)")
		code.Print("		x.Oneof = &SpecificationExtension_Number{Number: v}")
		code.Print("    case \"!!int\":")
		code.Print("        var v int64")
		code.Print("        v, matched = compiler.IntForScalarNode(in)")
		code.Print("		x.Oneof = &SpecificationExtension_Number{Number: float64(v)}")
		code.Print("	}")
		code.Print("	if matched {")
		code.Print("		// since the oneof matched one of its possibilities, discard any matching errors")
		code.Print("		errors = make([]error, 0)")
		code.Print("	}")
	} else if typeModel.Name == "DefaultType" {
		code.Print("	x := &DefaultType{}")
		code.Print("	matched := false")
		code.Print("	switch in.Tag {")
		code.Print("    case \"!!bool\":")
		code.Print("        var v bool")
		code.Print("        v, matched = compiler.BoolForScalarNode(in)")
		code.Print("		x.Oneof = &DefaultType_Boolean{Boolean: v}")
		code.Print("    case \"!!str\":")
		code.Print("        var v string")
		code.Print("        v, matched = compiler.StringForScalarNode(in)")
		code.Print("		x.Oneof = &DefaultType_String_{String_: v}")
		code.Print("    case \"!!float\":")
		code.Print("        var v float64")
		code.Print("        v, matched = compiler.FloatForScalarNode(in)")
		code.Print("		x.Oneof = &DefaultType_Number{Number: v}")
		code.Print("    case \"!!int\":")
		code.Print("        var v int64")
		code.Print("        v, matched = compiler.IntForScalarNode(in)")
		code.Print("		x.Oneof = &DefaultType_Number{Number: float64(v)}")
		code.Print("	}")
		code.Print("	if matched {")
		code.Print("		// since the oneof matched one of its possibilities, discard any matching errors")
		code.Print("		errors = make([]error, 0)")
		code.Print("	}")
	} else {
		oneOfWrapper := typeModel.OneOfWrapper

		code.Print("x := &%s{}", typeName)

		if oneOfWrapper {
			code.Print("matched := false")
		}

		unpackAtTop := !oneOfWrapper || len(typeModel.Required) > 0
		if unpackAtTop {
			code.Print("m, ok := compiler.UnpackMap(in)")
			code.Print("if !ok {")
			code.Print("  message := fmt.Sprintf(\"has unexpected value: %%+v (%%T)\", in, in)")
			code.Print("  errors = append(errors, compiler.NewError(context, message))")
			code.Print("} else {")
		}
		if len(typeModel.Required) > 0 {
			// verify that map includes all required keys
			keyString := ""
			sort.Strings(typeModel.Required)
			for _, k := range typeModel.Required {
				if keyString != "" {
					keyString += ","
				}
				keyString += "\""
				keyString += k
				keyString += "\""
			}
			code.Print("requiredKeys := []string{%s}", keyString)
			code.Print("missingKeys := compiler.MissingKeysInMap(m, requiredKeys)")
			code.Print("if len(missingKeys) > 0 {")
			code.Print("  message := fmt.Sprintf(\"is missing required %%s: %%+v\", compiler.PluralProperties(len(missingKeys)), strings.Join(missingKeys, \", \"))")
			code.Print("  errors = append(errors, compiler.NewError(context, message))")
			code.Print("}")
		}

		if !typeModel.Open {
			// verify that map has no unspecified keys
			allowedKeys := make([]string, 0)
			for _, property := range typeModel.Properties {
				if !property.Implicit {
					allowedKeys = append(allowedKeys, property.Name)
				}
			}
			sort.Strings(allowedKeys)
			allowedKeyString := ""
			for _, allowedKey := range allowedKeys {
				if allowedKeyString != "" {
					allowedKeyString += ","
				}
				allowedKeyString += "\""
				allowedKeyString += allowedKey
				allowedKeyString += "\""
			}
			allowedPatternString := ""
			if typeModel.OpenPatterns != nil {
				for _, pattern := range typeModel.OpenPatterns {
					if allowedPatternString != "" {
						allowedPatternString += ","
					}
					allowedPatternString += nameForPattern(regexPatterns, pattern)
				}
			}
			// verify that map includes only allowed keys and patterns
			code.Print("allowedKeys := []string{%s}", allowedKeyString)
			if len(allowedPatternString) > 0 {
				code.Print("allowedPatterns := []*regexp.Regexp{%s}", allowedPatternString)
			} else {
				code.Print("var allowedPatterns []*regexp.Regexp")

			}
			code.Print("invalidKeys := compiler.InvalidKeysInMap(m, allowedKeys, allowedPatterns)")
			code.Print("if len(invalidKeys) > 0 {")
			code.Print("  message := fmt.Sprintf(\"has invalid %%s: %%+v\", compiler.PluralProperties(len(invalidKeys)), strings.Join(invalidKeys, \", \"))")
			code.Print("  errors = append(errors, compiler.NewError(context, message))")
			code.Print("}")
		}

		var fieldNumber = 0
		for _, propertyModel := range typeModel.Properties {
			propertyName := propertyModel.Name
			fieldNumber++
			propertyType := propertyModel.Type
			if propertyType == "int" {
				propertyType = "int64"
			}
			var displayName = propertyName
			if displayName == "$ref" {
				displayName = "_ref"
			}
			if displayName == "$schema" {
				displayName = "_schema"
			}
			displayName = camelCaseToSnakeCase(displayName)

			var line = fmt.Sprintf("%s %s = %d;", propertyType, displayName, fieldNumber)
			if propertyModel.Repeated {
				line = "repeated " + line
			}
			code.Print("// " + line)

			fieldName := strings.Title(snakeCaseToCamelCase(propertyName))
			if propertyName == "$ref" {
				fieldName = "XRef"
			}

			typeModel, typeFound := domain.TypeModels[propertyType]
			if typeFound && !typeModel.IsPair {
				if propertyModel.Repeated {
					code.Print("v%d := compiler.MapValueForKey(m, \"%s\")", fieldNumber, propertyName)
					code.Print("if (v%d != nil) {", fieldNumber)
					code.Print("  // repeated %s", typeModel.Name)
					code.Print("  x.%s = make([]*%s, 0)", fieldName, typeModel.Name)
					code.Print("  a, ok := compiler.SequenceNodeForNode(v%d)", fieldNumber)
					code.Print("  if ok {")
					code.Print("    for _, item := range a.Content {")
					code.Print("      y, err := New%s(item, compiler.NewContext(\"%s\", item, context))", typeModel.Name, propertyName)
					code.Print("      if err != nil {")
					code.Print("        errors = append(errors, err)")
					code.Print("      }")
					code.Print("      x.%s = append(x.%s, y)", fieldName, fieldName)
					code.Print("    }")
					code.Print("  }")
					code.Print("}")
				} else {
					if oneOfWrapper {
						code.Print("{")
						if !unpackAtTop {
							code.Print("  m, ok := compiler.UnpackMap(in)")
							code.Print("  if ok {")
						}
						code.Print("    // errors might be ok here, they mean we just don't have the right subtype")
						code.Print("    t, matchingError := New%s(m, compiler.NewContext(\"%s\", m, context))", typeModel.Name, propertyName)
						code.Print("    if matchingError == nil {")
						code.Print("      x.Oneof = &%s_%s{%s: t}", parentTypeName, typeModel.Name, typeModel.Name)
						code.Print("      matched = true")
						code.Print("    } else {")
						code.Print("      errors = append(errors, matchingError)")
						code.Print("    }")
						if !unpackAtTop {
							code.Print("  }")
						}
						code.Print("}")
					} else {
						code.Print("v%d := compiler.MapValueForKey(m, \"%s\")", fieldNumber, propertyName)
						code.Print("if (v%d != nil) {", fieldNumber)
						code.Print("  var err error")
						code.Print("  x.%s, err = New%s(v%d, compiler.NewContext(\"%s\", v%d, context))",
							fieldName, typeModel.Name, fieldNumber, propertyName, fieldNumber)
						code.Print("  if err != nil {")
						code.Print("    errors = append(errors, err)")
						code.Print("  }")
						code.Print("}")
					}
				}
			} else if propertyType == "string" {
				if propertyModel.Repeated {
					code.Print("v%d := compiler.MapValueForKey(m, \"%s\")", fieldNumber, propertyName)
					code.Print("if (v%d != nil) {", fieldNumber)
					code.Print("  v, ok := compiler.SequenceNodeForNode(v%d)", fieldNumber)
					code.Print("  if ok {")
					code.Print("    x.%s = compiler.StringArrayForSequenceNode(v)", fieldName)
					code.Print("  } else {")
					code.Print("    message := fmt.Sprintf(\"has unexpected value for %s: %%s\", compiler.Display(v%d))", propertyName, fieldNumber)
					code.Print("    errors = append(errors, compiler.NewError(context, message))")
					code.Print("}")

					if propertyModel.StringEnumValues != nil {
						code.Print("// check for valid enum values")
						code.Print("// %+v", propertyModel.StringEnumValues)

						stringArrayLiteral := "[]string{"
						for i, item := range propertyModel.StringEnumValues {
							if i > 0 {
								stringArrayLiteral += ","
							}
							stringArrayLiteral += "\"" + item + "\""
						}
						stringArrayLiteral += "}"
						code.Print("if ok && !compiler.StringArrayContainsValues(%s, x.%s) {", stringArrayLiteral, fieldName)
						code.Print("  message := fmt.Sprintf(\"has unexpected value for %s: %%s\", compiler.Display(v%d))", propertyName, fieldNumber)
						code.Print("  errors = append(errors, compiler.NewError(context, message))")
						code.Print("}")
					}

					code.Print("}")
				} else {
					code.Print("v%d := compiler.MapValueForKey(m, \"%s\")", fieldNumber, propertyName)
					code.Print("if (v%d != nil) {", fieldNumber)
					code.Print("  x.%s, ok = compiler.StringForScalarNode(v%d)", fieldName, fieldNumber)
					code.Print("  if !ok {")
					code.Print("    message := fmt.Sprintf(\"has unexpected value for %s: %%s\", compiler.Display(v%d))", propertyName, fieldNumber)
					code.Print("    errors = append(errors, compiler.NewError(context, message))")
					code.Print("  }")

					if propertyModel.StringEnumValues != nil {
						code.Print("// check for valid enum values")
						code.Print("// %+v", propertyModel.StringEnumValues)

						stringArrayLiteral := "[]string{"
						for i, item := range propertyModel.StringEnumValues {
							if i > 0 {
								stringArrayLiteral += ","
							}
							stringArrayLiteral += "\"" + item + "\""
						}
						stringArrayLiteral += "}"

						code.Print("if ok && !compiler.StringArrayContainsValue(%s, x.%s) {", stringArrayLiteral, fieldName)
						code.Print("  message := fmt.Sprintf(\"has unexpected value for %s: %%s\", compiler.Display(v%d))", propertyName, fieldNumber)
						code.Print("  errors = append(errors, compiler.NewError(context, message))")
						code.Print("}")
					}
					code.Print("}")
				}
			} else if propertyType == "float" {
				code.Print("v%d := compiler.MapValueForKey(m, \"%s\")", fieldNumber, propertyName)
				code.Print("if (v%d != nil) {", fieldNumber)
				code.Print("  v, ok := compiler.FloatForScalarNode(v%d)", fieldNumber)
				code.Print("  if ok {")
				code.Print("    x.%s = v", fieldName)
				code.Print("  } else {")
				code.Print("    message := fmt.Sprintf(\"has unexpected value for %s: %%s\", compiler.Display(v%d))", propertyName, fieldNumber)
				code.Print("    errors = append(errors, compiler.NewError(context, message))")
				code.Print("  }")
				code.Print("}")
			} else if propertyType == "int64" {
				code.Print("v%d := compiler.MapValueForKey(m, \"%s\")", fieldNumber, propertyName)
				code.Print("if (v%d != nil) {", fieldNumber)
				code.Print("  t, ok := compiler.IntForScalarNode(v%d)", fieldNumber)
				code.Print("  if ok {")
				code.Print("    x.%s = int64(t)", fieldName)
				code.Print("  } else {")
				code.Print("    message := fmt.Sprintf(\"has unexpected value for %s: %%s\", compiler.Display(v%d))", propertyName, fieldNumber)
				code.Print("    errors = append(errors, compiler.NewError(context, message))")
				code.Print("  }")
				code.Print("}")
			} else if propertyType == "bool" {
				if oneOfWrapper {
					propertyName := "Boolean"
					code.Print("boolValue, ok := compiler.BoolForScalarNode(in)")
					code.Print("if ok {")
					code.Print("  x.Oneof = &%s_%s{%s: boolValue}", parentTypeName, propertyName, propertyName)
					code.Print("  matched = true")
					code.Print("}")
				} else {
					code.Print("v%d := compiler.MapValueForKey(m, \"%s\")", fieldNumber, propertyName)
					code.Print("if (v%d != nil) {", fieldNumber)
					code.Print("  x.%s, ok = compiler.BoolForScalarNode(v%d)", fieldName, fieldNumber)
					code.Print("  if !ok {")
					code.Print("    message := fmt.Sprintf(\"has unexpected value for %s: %%s\", compiler.Display(v%d))", propertyName, fieldNumber)
					code.Print("    errors = append(errors, compiler.NewError(context, message))")
					code.Print("  }")
					code.Print("}")
				}
			} else {
				mapTypeName := propertyModel.MapType
				if mapTypeName != "" {
					code.Print("// MAP: %s %s", mapTypeName, propertyModel.Pattern)
					if mapTypeName == "string" {
						code.Print("x.%s = make([]*NamedString, 0)", fieldName)
					} else {
						code.Print("x.%s = make([]*Named%s, 0)", fieldName, mapTypeName)
					}
					code.Print("for i := 0; i < len(m.Content); i += 2 {")
					code.Print("k, ok := compiler.StringForScalarNode(m.Content[i])")
					code.Print("if ok {")
					code.Print("v := m.Content[i+1]")
					if pattern := propertyModel.Pattern; pattern != "" {
						if inline, ok := regexPatterns.SpecialCaseExpression(pattern, "k"); ok {
							code.Print("if %s {", inline)
						} else {
							code.Print("if %s.MatchString(k) {", nameForPattern(regexPatterns, pattern))
						}
					}

					code.Print("pair := &Named" + strings.Title(mapTypeName) + "{}")
					code.Print("pair.Name = k")

					if mapTypeName == "string" {
						code.Print("pair.Value, _ = compiler.StringForScalarNode(v)")
					} else if mapTypeName == "Any" {
						code.Print("result := &Any{}")
						code.Print("handled, resultFromExt, err := compiler.CallExtension(context, v, k)")
						code.Print("if handled {")
						code.Print("	if err != nil {")
						code.Print("		errors = append(errors, err)")
						code.Print("	} else {")
						code.Print("		bytes := compiler.Marshal(v)")
						code.Print("		result.Yaml = string(bytes)")
						code.Print("		result.Value = resultFromExt")
						code.Print("		pair.Value = result")
						code.Print("	}")
						code.Print("} else {")
						code.Print("	pair.Value, err = NewAny(v, compiler.NewContext(k, v, context))")
						code.Print("	if err != nil {")
						code.Print("		errors = append(errors, err)")
						code.Print("	}")
						code.Print("}")
					} else {
						code.Print("var err error")
						code.Print("pair.Value, err = New%s(v, compiler.NewContext(k, v, context))", mapTypeName)
						code.Print("if err != nil {")
						code.Print("  errors = append(errors, err)")
						code.Print("}")
					}
					code.Print("x.%s = append(x.%s, pair)", fieldName, fieldName)
					if propertyModel.Pattern != "" {
						code.Print("}")
					}
					code.Print("}")
					code.Print("}")
				} else {
					code.Print("// TODO: %s", propertyType)
				}
			}
		}
		if unpackAtTop {
			code.Print("}")
		}
		if oneOfWrapper {
			code.Print("if matched {")
			code.Print("    // since the oneof matched one of its possibilities, discard any matching errors")
			code.Print("	errors = make([]error, 0)")
			generateMatchErrors := true // TODO: enable this and update tests for new error messages
			if generateMatchErrors {
				code.Print("} else {")
				code.Print("    message := fmt.Sprintf(\"contains an invalid %s\")", typeName)
				code.Print("    err := compiler.NewError(context, message)")
				code.Print("    errors = []error{err}")
			}
			code.Print("}")
		}
	}

	// assumes that the return value is in a variable named "x"
	code.Print("  return x, compiler.NewErrorGroupOrNil(errors)")
	code.Print("}\n")
}

// ResolveReferences() methods
func (domain *Domain) generateResolveReferencesMethodsForType(code *printer.Code, typeName string) {
	code.Print("// ResolveReferences resolves references found inside %s objects.", typeName)
	code.Print("func (m *%s) ResolveReferences(root string) (*yaml.Node, error) {", typeName)
	code.Print("errors := make([]error, 0)")

	typeModel := domain.TypeModels[typeName]
	if typeModel.OneOfWrapper {
		// call ResolveReferences on whatever is in the Oneof.
		for _, propertyModel := range typeModel.Properties {
			propertyType := propertyModel.Type
			_, typeFound := domain.TypeModels[propertyType]
			if typeFound {
				code.Print("{")
				code.Print("p, ok := m.Oneof.(*%s_%s)", typeName, propertyType)
				code.Print("if ok {")
				if propertyType == "JsonReference" { // Special case for OpenAPI
					code.Print("info, err := p.%s.ResolveReferences(root)", propertyType)
					code.Print("if err != nil {")
					code.Print("  return nil, err")
					code.Print("} else if info != nil {")
					code.Print("  n, err := New%s(info, nil)", typeName)
					code.Print("  if err != nil {")
					code.Print("    return nil, err")
					code.Print("  } else if n != nil {")
					code.Print("    *m = *n")
					code.Print("    return nil, nil")
					code.Print("  }")
					code.Print("}")
				} else {
					code.Print("_, err := p.%s.ResolveReferences(root)", propertyType)
					code.Print("if err != nil {")
					code.Print("	return nil, err")
					code.Print("}")
				}
				code.Print("}")
				code.Print("}")
			}
		}
	} else {
		for _, propertyModel := range typeModel.Properties {
			propertyName := propertyModel.Name
			var displayName = propertyName
			if displayName == "$ref" {
				displayName = "_ref"
			}
			if displayName == "$schema" {
				displayName = "_schema"
			}
			displayName = camelCaseToSnakeCase(displayName)

			fieldName := strings.Title(propertyName)
			if propertyName == "$ref" {
				fieldName = "XRef"
				code.Print("if m.XRef != \"\" {")
				//code.Print("log.Printf(\"%s reference to resolve %%+v\", m.XRef)", typeName)
				code.Print("info, err := compiler.ReadInfoForRef(root, m.XRef)")

				code.Print("if err != nil {")
				code.Print("	return nil, err")
				code.Print("}")
				//code.Print("log.Printf(\"%%+v\", info)")

				if len(typeModel.Properties) > 1 {
					code.Print("if info != nil {")
					code.Print("  replacement, err := New%s(info, nil)", typeName)
					code.Print("  if err == nil {")
					code.Print("    *m = *replacement")
					code.Print("    return m.ResolveReferences(root)")
					code.Print("  }")
					code.Print("}")
				}

				code.Print("return info, nil")
				code.Print("}")
			}

			if !propertyModel.Repeated {
				propertyType := propertyModel.Type
				typeModel, typeFound := domain.TypeModels[propertyType]
				if typeFound && !typeModel.IsPair {
					code.Print("if m.%s != nil {", fieldName)
					code.Print("    _, err := m.%s.ResolveReferences(root)", fieldName)
					code.Print("    if err != nil {")
					code.Print("       errors = append(errors, err)")
					code.Print("    }")
					code.Print("}")
				}
			} else {
				propertyType := propertyModel.Type
				_, typeFound := domain.TypeModels[propertyType]
				if typeFound {
					code.Print("for _, item := range m.%s {", fieldName)
					code.Print("if item != nil {")
					code.Print("  _, err := item.ResolveReferences(root)")
					code.Print("  if err != nil {")
					code.Print("     errors = append(errors, err)")
					code.Print("  }")
					code.Print("}")
					code.Print("}")
				}

			}
		}
	}
	code.Print("  return nil, compiler.NewErrorGroupOrNil(errors)")
	code.Print("}\n")
}

// ToRawInfo() methods
func (domain *Domain) generateToRawInfoMethodForType(code *printer.Code, typeName string) {
	code.Print("// ToRawInfo returns a description of %s suitable for JSON or YAML export.", typeName)
	code.Print("func (m *%s) ToRawInfo() *yaml.Node {", typeName)
	typeModel := domain.TypeModels[typeName]
	if typeName == "Any" {
		code.Print("var err error")
		code.Print("var node yaml.Node")
		code.Print("err = yaml.Unmarshal([]byte(m.Yaml), &node)")
		code.Print("if err == nil {")
		code.Print("	if node.Kind == yaml.DocumentNode {")
		code.Print("		return node.Content[0]")
		code.Print("	}")
		code.Print("	return &node")
		code.Print("}")
		code.Print("return compiler.NewNullNode()")
	} else if typeName == "StringArray" {
		code.Print("return compiler.NewSequenceNodeForStringArray(m.Value)")
	} else if typeModel.OneOfWrapper {
		code.Print("// ONE OF WRAPPER")
		code.Print("// %s", typeModel.Name)
		for i, item := range typeModel.Properties {
			code.Print("// %+v", *item)
			if item.Type == "float" {
				code.Print("if v%d, ok := m.GetOneof().(*%s_Number); ok {", i, typeName)
				code.Print("return compiler.NewScalarNodeForFloat(v%d.Number)", i)
				code.Print("}")
			} else if item.Type == "bool" {
				code.Print("if v%d, ok := m.GetOneof().(*%s_Boolean); ok {", i, typeName)
				code.Print("return compiler.NewScalarNodeForBool(v%d.Boolean)", i)
				code.Print("}")
			} else if item.Type == "string" {
				code.Print("if v%d, ok := m.GetOneof().(*%s_String_); ok {", i, typeName)
				code.Print("return compiler.NewScalarNodeForString(v%d.String_)", i)
				code.Print("}")
			} else {
				code.Print("v%d := m.Get%s()", i, item.Type)
				code.Print("if v%d != nil {", i)
				code.Print(" return v%d.ToRawInfo()", i)
				code.Print("}")
			}
		}
		code.Print("return compiler.NewNullNode()")
	} else {
		code.Print("info := compiler.NewMappingNode()")
		code.Print("if m == nil {return info}")
		for _, propertyModel := range typeModel.Properties {
			isRequired := typeModel.IsRequired(propertyModel.Name)
			switch propertyModel.Type {
			case "string":
				propertyName := propertyModel.Name
				if !propertyModel.Repeated {
					code.PrintIf(isRequired, "// always include this required field.")
					code.PrintIf(!isRequired, "if m.%s != \"\" {", propertyModel.FieldName())
					code.Print("info.Content = append(info.Content, compiler.NewScalarNodeForString(\"%s\"))", propertyName)
					code.Print("info.Content = append(info.Content, compiler.NewScalarNodeForString(m.%s))", propertyModel.FieldName())
					code.PrintIf(!isRequired, "}")
				} else {
					code.Print("if len(m.%s) != 0 {", propertyModel.FieldName())
					code.Print("info.Content = append(info.Content, compiler.NewScalarNodeForString(\"%s\"))", propertyName)
					code.Print("info.Content = append(info.Content, compiler.NewSequenceNodeForStringArray(m.%s))", propertyModel.FieldName())
					code.Print("}")
				}
			case "bool":
				propertyName := propertyModel.Name
				if !propertyModel.Repeated {
					code.PrintIf(isRequired, "// always include this required field.")
					code.PrintIf(!isRequired, "if m.%s != false {", propertyModel.FieldName())
					code.Print("info.Content = append(info.Content, compiler.NewScalarNodeForString(\"%s\"))", propertyName)
					code.Print("info.Content = append(info.Content, compiler.NewScalarNodeForBool(m.%s))", propertyModel.FieldName())
					code.PrintIf(!isRequired, "}")
				} else {
					code.Print("if len(m.%s) != 0 {", propertyModel.FieldName())
					code.Print("info.Content = append(info.Content, compiler.NewScalarNodeForString(\"%s\"))", propertyName)
					code.Print("info.Content = append(info.Content, compiler.NewSequenceNodeForBoolArray(m.%s))", propertyModel.FieldName())
					code.Print("}")
				}
			case "int":
				propertyName := propertyModel.Name
				if !propertyModel.Repeated {
					code.PrintIf(isRequired, "// always include this required field.")
					code.PrintIf(!isRequired, "if m.%s != 0 {", propertyModel.FieldName())
					code.Print("info.Content = append(info.Content, compiler.NewScalarNodeForString(\"%s\"))", propertyName)
					code.Print("info.Content = append(info.Content, compiler.NewScalarNodeForInt(m.%s))", propertyModel.FieldName())
					code.PrintIf(!isRequired, "}")
				} else {
					code.Print("if len(m.%s) != 0 {", propertyModel.FieldName())
					code.Print("info.Content = append(info.Content, compiler.NewScalarNodeForString(\"%s\"))", propertyName)
					code.Print("info.Content = append(info.Content, compiler.NewSequenceNodeForIntArray(m.%s))", propertyModel.FieldName())
					code.Print("}")
				}
			case "float":
				propertyName := propertyModel.Name
				if !propertyModel.Repeated {
					code.PrintIf(isRequired, "// always include this required field.")
					code.PrintIf(!isRequired, "if m.%s != 0.0 {", propertyModel.FieldName())
					code.Print("info.Content = append(info.Content, compiler.NewScalarNodeForString(\"%s\"))", propertyName)
					code.Print("info.Content = append(info.Content, compiler.NewScalarNodeForFloat(m.%s))", propertyModel.FieldName())
					code.PrintIf(!isRequired, "}")
				} else {
					code.Print("if len(m.%s) != 0 {", propertyModel.FieldName())
					code.Print("info.Content = append(info.Content, compiler.NewScalarNodeForString(\"%s\"))", propertyName)
					code.Print("info.Content = append(info.Content, compiler.NewSequenceNodeForFloatArray(m.%s))", propertyModel.FieldName())
					code.Print("}")
				}
			default:
				propertyName := propertyModel.Name
				if propertyName == "value" && propertyModel.Type != "Any" {
					code.Print("// %+v", propertyModel)
				} else if !propertyModel.Repeated {
					code.PrintIf(isRequired, "// always include this required field.")
					code.PrintIf(!isRequired, "if m.%s != nil {", propertyModel.FieldName())
					if propertyModel.Type == "TypeItem" {
						code.Print("if len(m.Type.Value) == 1 {")
						code.Print("info.Content = append(info.Content, compiler.NewScalarNodeForString(\"type\"))")
						code.Print("info.Content = append(info.Content, compiler.NewScalarNodeForString(m.Type.Value[0]))")
						code.Print("} else {")
						code.Print("info.Content = append(info.Content, compiler.NewScalarNodeForString(\"type\"))")
						code.Print("info.Content = append(info.Content, compiler.NewSequenceNodeForStringArray(m.Type.Value))")
						code.Print("}")
					} else if propertyModel.Type == "ItemsItem" {
						code.Print("items := compiler.NewSequenceNode()")
						if domain.Version == "v2" {
							code.Print("for _, item := range m.Items.Schema {")
						} else {
							code.Print("for _, item := range m.Items.SchemaOrReference {")
						}
						code.Print("	items.Content = append(items.Content, item.ToRawInfo())")
						code.Print("}")
						code.Print("if len(items.Content) == 1 {items = items.Content[0]}")
						code.Print("info.Content = append(info.Content, compiler.NewScalarNodeForString(\"items\"))")
						code.Print("info.Content = append(info.Content, items)")
					} else {
						code.Print("info.Content = append(info.Content, compiler.NewScalarNodeForString(\"%s\"))", propertyName)
						code.Print("info.Content = append(info.Content, m.%s.ToRawInfo())", propertyModel.FieldName())
					}
					code.PrintIf(!isRequired, "}")
				} else if propertyModel.MapType == "string" {
					code.Print("if m.%s != nil {", propertyModel.FieldName())
					code.Print("for _, item := range m.%s {", propertyModel.FieldName())
					code.Print("info.Content = append(info.Content, compiler.NewScalarNodeForString(item.Name))")
					code.Print("info.Content = append(info.Content, compiler.NewScalarNodeForString(item.Value))")
					code.Print("}")
					code.Print("}")
				} else if propertyModel.MapType != "" {
					code.Print("if m.%s != nil {", propertyModel.FieldName())
					code.Print("for _, item := range m.%s {", propertyModel.FieldName())
					code.Print("info.Content = append(info.Content, compiler.NewScalarNodeForString(item.Name))")
					code.Print("info.Content = append(info.Content, item.Value.ToRawInfo())")
					code.Print("}")
					code.Print("}")
				} else {
					code.Print("if len(m.%s) != 0 {", propertyModel.FieldName())
					code.Print("items := compiler.NewSequenceNode()")
					code.Print("for _, item := range m.%s {", propertyModel.FieldName())
					code.Print("items.Content = append(items.Content, item.ToRawInfo())")
					code.Print("}")
					code.Print("info.Content = append(info.Content, compiler.NewScalarNodeForString(\"%s\"))", propertyName)
					code.Print("info.Content = append(info.Content, items)")
					code.Print("}")
				}
			}
		}
		code.Print("return info")
	}
	code.Print("}\n")
}

func (domain *Domain) generateConstantVariables(code *printer.Code, regexPatterns *patternNames) {
	names := regexPatterns.Names()
	if len(names) == 0 {
		return
	}
	var sortedNames []string
	for name := range names {
		sortedNames = append(sortedNames, name)
	}
	sort.Strings(sortedNames)
	code.Print("var (")
	for _, name := range sortedNames {
		code.Print("%s = regexp.MustCompile(\"%s\")", name, escapeSlashes(names[name]))
	}
	code.Print(")\n")
}
