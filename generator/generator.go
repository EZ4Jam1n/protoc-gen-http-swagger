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
//

/*
 * Copyright 2024 CloudWeGo Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package generator

import (
	"fmt"
	"google.golang.org/protobuf/runtime/protoimpl"
	"log"
	"protoc-gen-http-swagger/protobuf/api"
	openapi_v3 "protoc-gen-http-swagger/protobuf/openapi"
	"regexp"
	"sort"
	"strings"

	"google.golang.org/genproto/googleapis/api/annotations"
	status_pb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	any_pb "google.golang.org/protobuf/types/known/anypb"

	wk "protoc-gen-http-swagger/generator/wellknown"
)

type Configuration struct {
	Version         *string
	Title           *string
	Description     *string
	Naming          *string
	FQSchemaNaming  *bool
	EnumType        *string
	CircularDepth   *int
	DefaultResponse *bool
	OutputMode      *string
}

const (
	infoURL = "https://github.com/hertz-contrib/swagger-generate/hertz/protoc-gen-http-swagger"
)

// In order to dynamically add google.rpc.Status responses we need
// to know the message descriptors for google.rpc.Status as well
// as google.protobuf.Any.
var statusProtoDesc = (&status_pb.Status{}).ProtoReflect().Descriptor()
var anyProtoDesc = (&any_pb.Any{}).ProtoReflect().Descriptor()

// OpenAPIv3Generator holds internal state needed to generate an OpenAPIv3 document for a transcoded Protocol Buffer service.
type OpenAPIv3Generator struct {
	conf   Configuration
	plugin *protogen.Plugin

	inputFiles        []*protogen.File
	reflect           *OpenAPIv3Reflector
	generatedSchemas  []string // Names of schemas that have already been generated.
	linterRulePattern *regexp.Regexp
	pathPattern       *regexp.Regexp
	namedPathPattern  *regexp.Regexp
}

// NewOpenAPIv3Generator creates a new generator for a protoc plugin invocation.
func NewOpenAPIv3Generator(plugin *protogen.Plugin, conf Configuration, inputFiles []*protogen.File) *OpenAPIv3Generator {
	return &OpenAPIv3Generator{
		conf:   conf,
		plugin: plugin,

		inputFiles:        inputFiles,
		reflect:           NewOpenAPIv3Reflector(conf),
		generatedSchemas:  make([]string, 0),
		linterRulePattern: regexp.MustCompile(`\(-- .* --\)`),
		pathPattern:       regexp.MustCompile("{([^=}]+)}"),
		namedPathPattern:  regexp.MustCompile("{(.+)=(.+)}"),
	}
}

// Run runs the generator.
func (g *OpenAPIv3Generator) Run(outputFile *protogen.GeneratedFile) error {
	d := g.buildDocument()
	bytes, err := d.YAMLValue("Generated with protoc-gen-http-swagger\n" + infoURL)
	if err != nil {
		return fmt.Errorf("failed to marshal yaml: %s", err.Error())
	}
	if _, err = outputFile.Write(bytes); err != nil {
		return fmt.Errorf("failed to write yaml: %s", err.Error())
	}
	return nil
}

// buildDocument builds an OpenAPIv3 document for a plugin request.
func (g *OpenAPIv3Generator) buildDocument() *openapi_v3.Document {
	d := &openapi_v3.Document{}

	d.Openapi = "3.0.3"
	d.Info = &openapi_v3.Info{
		Version:     *g.conf.Version,
		Title:       *g.conf.Title,
		Description: *g.conf.Description,
	}

	d.Paths = &openapi_v3.Paths{}
	d.Components = &openapi_v3.Components{
		Schemas: &openapi_v3.SchemasOrReferences{
			AdditionalProperties: []*openapi_v3.NamedSchemaOrReference{},
		},
	}

	// Go through the files and add the services to the documents, keeping
	// track of which schemas are referenced in the response so we can
	// add them later.
	for _, file := range g.inputFiles {
		if file.Generate {
			// Merge any `Document` annotations with the current
			extDocument := proto.GetExtension(file.Desc.Options(), openapi_v3.E_Document)
			if extDocument != nil {
				proto.Merge(d, extDocument.(*openapi_v3.Document))
			}
			g.addPathsToDocument(d, file.Services)
		}
	}

	// While we have required schemas left to generate, go through the files again
	// looking for the related message and adding them to the document if required.
	for len(g.reflect.requiredSchemas) > 0 {
		count := len(g.reflect.requiredSchemas)
		for _, file := range g.plugin.Files {
			g.addSchemasForMessagesToDocumentV3(d, file.Messages)
		}
		g.reflect.requiredSchemas = g.reflect.requiredSchemas[count:len(g.reflect.requiredSchemas)]
	}

	// If there is only 1 service, then use it's title for the
	// document, if the document is missing it.
	if len(d.Tags) == 1 {
		if d.Info.Title == "" && d.Tags[0].Name != "" {
			d.Info.Title = d.Tags[0].Name + " API"
		}
		if d.Info.Description == "" {
			d.Info.Description = d.Tags[0].Description
		}
		d.Tags[0].Description = ""
	}

	var allServers []string

	// If paths methods has servers, but they're all the same, then move servers to path level
	for _, path := range d.Paths.Path {
		var servers []string
		// Only 1 server will ever be set, per method, by the generator

		if path.Value.Get != nil && len(path.Value.Get.Servers) == 1 {
			servers = appendUnique(servers, path.Value.Get.Servers[0].Url)
			allServers = appendUnique(allServers, path.Value.Get.Servers[0].Url)
		}
		if path.Value.Post != nil && len(path.Value.Post.Servers) == 1 {
			servers = appendUnique(servers, path.Value.Post.Servers[0].Url)
			allServers = appendUnique(allServers, path.Value.Post.Servers[0].Url)
		}
		if path.Value.Put != nil && len(path.Value.Put.Servers) == 1 {
			servers = appendUnique(servers, path.Value.Put.Servers[0].Url)
			allServers = appendUnique(allServers, path.Value.Put.Servers[0].Url)
		}
		if path.Value.Delete != nil && len(path.Value.Delete.Servers) == 1 {
			servers = appendUnique(servers, path.Value.Delete.Servers[0].Url)
			allServers = appendUnique(allServers, path.Value.Delete.Servers[0].Url)
		}
		if path.Value.Patch != nil && len(path.Value.Patch.Servers) == 1 {
			servers = appendUnique(servers, path.Value.Patch.Servers[0].Url)
			allServers = appendUnique(allServers, path.Value.Patch.Servers[0].Url)
		}

		if len(servers) == 1 {
			path.Value.Servers = []*openapi_v3.Server{{Url: servers[0]}}

			if path.Value.Get != nil {
				path.Value.Get.Servers = nil
			}
			if path.Value.Post != nil {
				path.Value.Post.Servers = nil
			}
			if path.Value.Put != nil {
				path.Value.Put.Servers = nil
			}
			if path.Value.Delete != nil {
				path.Value.Delete.Servers = nil
			}
			if path.Value.Patch != nil {
				path.Value.Patch.Servers = nil
			}
		}
	}

	// Set all servers on API level
	if len(allServers) > 0 {
		d.Servers = []*openapi_v3.Server{}
		for _, server := range allServers {
			d.Servers = append(d.Servers, &openapi_v3.Server{Url: server})
		}
	}

	// If there is only 1 server, we can safely remove all path level servers
	if len(allServers) == 1 {
		for _, path := range d.Paths.Path {
			path.Value.Servers = nil
		}
	}

	// Sort the tags.
	{
		pairs := d.Tags
		sort.Slice(pairs, func(i, j int) bool {
			return pairs[i].Name < pairs[j].Name
		})
		d.Tags = pairs
	}
	// Sort the paths.
	{
		pairs := d.Paths.Path
		sort.Slice(pairs, func(i, j int) bool {
			return pairs[i].Name < pairs[j].Name
		})
		d.Paths.Path = pairs
	}
	// Sort the schemas.
	{
		pairs := d.Components.Schemas.AdditionalProperties
		sort.Slice(pairs, func(i, j int) bool {
			return pairs[i].Name < pairs[j].Name
		})
		d.Components.Schemas.AdditionalProperties = pairs
	}
	return d
}

// filterCommentString removes linter rules from comments.
func (g *OpenAPIv3Generator) filterCommentString(c protogen.Comments) string {
	comment := g.linterRulePattern.ReplaceAllString(string(c), "")
	return strings.TrimSpace(comment)
}

// Note that fields which are mapped to URL query parameters must have a primitive type
// or a repeated primitive type or a non-repeated message type.
// In the case of a repeated type, the parameter can be repeated in the URL as ...?param=A&param=B.
// In the case of a message type, each field of the message is mapped to a separate parameter,
// such as ...?foo.a=A&foo.b=B&foo.c=C.
// There are exceptions:
// - for wrapper types it will use the same representation as the wrapped primitive type in JSON
// - for google.protobuf.timestamp type it will be serialized as a string
//
// maps, Struct and Empty can NOT be used
// messages can have any number of sub messages - including circular (e.g. sub.subsub.sub.subsub.id)

// buildQueryParams extracts any valid query params, including sub and recursive messages
func (g *OpenAPIv3Generator) buildQueryParams(field *protogen.Field) []*openapi_v3.ParameterOrReference {
	depths := map[string]int{}
	return g._buildQueryParams(field, depths)
}

// depths are used to keep track of how many times a message's fields has been seen
func (g *OpenAPIv3Generator) _buildQueryParams(field *protogen.Field, depths map[string]int) []*openapi_v3.ParameterOrReference {
	var parameters []*openapi_v3.ParameterOrReference

	queryFieldName := g.reflect.formatFieldName(field.Desc)
	fieldDescription := g.filterCommentString(field.Comments.Leading)

	if field.Desc.IsMap() {
		// Map types are not allowed in query parameteres
		return parameters

	} else if field.Desc.Kind() == protoreflect.MessageKind {
		typeName := g.reflect.fullMessageTypeName(field.Desc.Message())

		switch typeName {
		case ".google.protobuf.Value":
			fieldSchema := g.reflect.schemaOrReferenceForField(field.Desc)
			parameters = append(parameters,
				&openapi_v3.ParameterOrReference{
					Oneof: &openapi_v3.ParameterOrReference_Parameter{
						Parameter: &openapi_v3.Parameter{
							Name:        queryFieldName,
							In:          "query",
							Description: fieldDescription,
							Required:    false,
							Schema:      fieldSchema,
						},
					},
				})
			return parameters

		case ".google.protobuf.BoolValue", ".google.protobuf.BytesValue", ".google.protobuf.Int32Value", ".google.protobuf.UInt32Value",
			".google.protobuf.StringValue", ".google.protobuf.Int64Value", ".google.protobuf.UInt64Value", ".google.protobuf.FloatValue",
			".google.protobuf.DoubleValue":
			valueField := getValueField(field.Message.Desc)
			fieldSchema := g.reflect.schemaOrReferenceForField(valueField)
			parameters = append(parameters,
				&openapi_v3.ParameterOrReference{
					Oneof: &openapi_v3.ParameterOrReference_Parameter{
						Parameter: &openapi_v3.Parameter{
							Name:        queryFieldName,
							In:          "query",
							Description: fieldDescription,
							Required:    false,
							Schema:      fieldSchema,
						},
					},
				})
			return parameters

		case ".google.protobuf.Timestamp":
			fieldSchema := g.reflect.schemaOrReferenceForMessage(field.Message.Desc)
			parameters = append(parameters,
				&openapi_v3.ParameterOrReference{
					Oneof: &openapi_v3.ParameterOrReference_Parameter{
						Parameter: &openapi_v3.Parameter{
							Name:        queryFieldName,
							In:          "query",
							Description: fieldDescription,
							Required:    false,
							Schema:      fieldSchema,
						},
					},
				})
			return parameters
		case ".google.protobuf.Duration":
			fieldSchema := g.reflect.schemaOrReferenceForMessage(field.Message.Desc)
			parameters = append(parameters,
				&openapi_v3.ParameterOrReference{
					Oneof: &openapi_v3.ParameterOrReference_Parameter{
						Parameter: &openapi_v3.Parameter{
							Name:        queryFieldName,
							In:          "query",
							Description: fieldDescription,
							Required:    false,
							Schema:      fieldSchema,
						},
					},
				})
			return parameters
		}

		if field.Desc.IsList() {
			// Only non-repeated message types are valid
			return parameters
		}

		// Represent field masks directly as strings (don't expand them).
		if typeName == ".google.protobuf.FieldMask" {
			fieldSchema := g.reflect.schemaOrReferenceForField(field.Desc)
			parameters = append(parameters,
				&openapi_v3.ParameterOrReference{
					Oneof: &openapi_v3.ParameterOrReference_Parameter{
						Parameter: &openapi_v3.Parameter{
							Name:        queryFieldName,
							In:          "query",
							Description: fieldDescription,
							Required:    false,
							Schema:      fieldSchema,
						},
					},
				})
			return parameters
		}

		// Sub messages are allowed, even circular, as long as the final type is a primitive.
		// Go through each of the sub message fields
		for _, subField := range field.Message.Fields {
			subFieldFullName := string(subField.Desc.FullName())
			seen, ok := depths[subFieldFullName]
			if !ok {
				depths[subFieldFullName] = 0
			}

			if seen < *g.conf.CircularDepth {
				depths[subFieldFullName]++
				subParams := g._buildQueryParams(subField, depths)
				for _, subParam := range subParams {
					if param, ok := subParam.Oneof.(*openapi_v3.ParameterOrReference_Parameter); ok {
						param.Parameter.Name = queryFieldName + "." + param.Parameter.Name
						parameters = append(parameters, subParam)
					}
				}
			}
		}

	} else if field.Desc.Kind() != protoreflect.GroupKind {
		// schemaOrReferenceForField also handles array types
		fieldSchema := g.reflect.schemaOrReferenceForField(field.Desc)

		parameters = append(parameters,
			&openapi_v3.ParameterOrReference{
				Oneof: &openapi_v3.ParameterOrReference_Parameter{
					Parameter: &openapi_v3.Parameter{
						Name:        queryFieldName,
						In:          "query",
						Description: fieldDescription,
						Required:    false,
						Schema:      fieldSchema,
					},
				},
			})
	}

	return parameters
}

func (g *OpenAPIv3Generator) getSchemaByOption(inputMessage *protogen.Message, bodyType *protoimpl.ExtensionInfo) *openapi_v3.Schema {

	// Build an array holding the fields of the message.
	definitionProperties := &openapi_v3.Properties{
		AdditionalProperties: make([]*openapi_v3.NamedSchemaOrReference, 0),
	}
	// Merge any `Schema` annotations with the current
	extSchema := proto.GetExtension(inputMessage.Desc.Options(), openapi_v3.E_Schema)
	var allRequired []string
	if extSchema != nil {
		if extSchema.(*openapi_v3.Schema) != nil {
			if extSchema.(*openapi_v3.Schema).Required != nil {
				allRequired = extSchema.(*openapi_v3.Schema).Required
			}
		}
	}
	var required []string
	for _, field := range inputMessage.Fields {
		if ext := proto.GetExtension(field.Desc.Options(), bodyType); ext != "" {
			if contains(allRequired, field.Desc.TextName()) {
				required = append(required, field.Desc.TextName())
			}

			// Get the field description from the comments.
			description := g.filterCommentString(field.Comments.Leading)
			// Check the field annotations to see if this is a readonly or writeonly field.
			inputOnly := false
			outputOnly := false
			extension := proto.GetExtension(field.Desc.Options(), annotations.E_FieldBehavior)
			if extension != nil {
				switch v := extension.(type) {
				case []annotations.FieldBehavior:
					for _, vv := range v {
						switch vv {
						case annotations.FieldBehavior_OUTPUT_ONLY:
							outputOnly = true
						case annotations.FieldBehavior_INPUT_ONLY:
							inputOnly = true
						case annotations.FieldBehavior_REQUIRED:
							required = append(required, g.reflect.formatFieldName(field.Desc))
						}
					}
				default:
					log.Printf("unsupported extension type %T", extension)
				}
			}

			// The field is either described by a reference or a schema.
			fieldSchema := g.reflect.schemaOrReferenceForField(field.Desc)
			if fieldSchema == nil {
				continue
			}

			// If this field has siblings and is a $ref now, create a new schema use `allOf` to wrap it
			wrapperNeeded := inputOnly || outputOnly || description != ""
			if wrapperNeeded {
				if _, ok := fieldSchema.Oneof.(*openapi_v3.SchemaOrReference_Reference); ok {
					fieldSchema = &openapi_v3.SchemaOrReference{Oneof: &openapi_v3.SchemaOrReference_Schema{Schema: &openapi_v3.Schema{
						AllOf: []*openapi_v3.SchemaOrReference{fieldSchema},
					}}}
				}
			}

			if schema, ok := fieldSchema.Oneof.(*openapi_v3.SchemaOrReference_Schema); ok {
				schema.Schema.Description = description
				schema.Schema.ReadOnly = outputOnly
				schema.Schema.WriteOnly = inputOnly

				// Merge any `Property` annotations with the current
				extProperty := proto.GetExtension(field.Desc.Options(), openapi_v3.E_Property)
				if extProperty != nil {
					proto.Merge(schema.Schema, extProperty.(*openapi_v3.Schema))
				}
			}

			definitionProperties.AdditionalProperties = append(
				definitionProperties.AdditionalProperties,
				&openapi_v3.NamedSchemaOrReference{
					Name:  g.reflect.formatFieldName(field.Desc),
					Value: fieldSchema,
				},
			)
		}
	}

	schema := &openapi_v3.Schema{
		Type:       "object",
		Properties: definitionProperties,
		//Required:   required,
	}

	// Merge any `Schema` annotations with the current
	extSchema = proto.GetExtension(inputMessage.Desc.Options(), openapi_v3.E_Schema)
	if extSchema != nil {
		proto.Merge(schema, extSchema.(*openapi_v3.Schema))
	}

	schema.Required = required
	return schema
}

func (g *OpenAPIv3Generator) buildOperation(
	d *openapi_v3.Document,
	methodName string,
	operationID string,
	tagName string,
	description string,
	defaultHost string,
	path string,
	inputMessage *protogen.Message,
	outputMessage *protogen.Message) (*openapi_v3.Operation, string) {

	// Parameters array to hold all parameter objects
	var parameters []*openapi_v3.ParameterOrReference

	// Iterate through each field in the input message
	for _, field := range inputMessage.Fields {

		var paramName, paramIn, paramDesc string
		var fieldSchema *openapi_v3.SchemaOrReference
		required := false
		var ext any
		// Check for each type of extension (query, path, cookie, header)
		if ext = proto.GetExtension(field.Desc.Options(), api.E_Query); ext != "" {
			paramName = proto.GetExtension(field.Desc.Options(), api.E_Query).(string)
			paramIn = "query"
			paramDesc = g.filterCommentString(field.Comments.Leading)
			fieldSchema = g.reflect.schemaOrReferenceForField(field.Desc)
			if schema, ok := fieldSchema.Oneof.(*openapi_v3.SchemaOrReference_Schema); ok {
				// Merge any `Property` annotations with the current
				extProperty := proto.GetExtension(field.Desc.Options(), openapi_v3.E_Property)
				if extProperty != nil {
					proto.Merge(schema.Schema, extProperty.(*openapi_v3.Schema))
				}
			}
		} else if ext = proto.GetExtension(field.Desc.Options(), api.E_Path); ext != "" {
			paramName = proto.GetExtension(field.Desc.Options(), api.E_Path).(string)
			paramIn = "path"
			paramDesc = g.filterCommentString(field.Comments.Leading)
			fieldSchema = g.reflect.schemaOrReferenceForField(field.Desc)
			if schema, ok := fieldSchema.Oneof.(*openapi_v3.SchemaOrReference_Schema); ok {
				// Merge any `Property` annotations with the current
				extProperty := proto.GetExtension(field.Desc.Options(), openapi_v3.E_Property)
				if extProperty != nil {
					proto.Merge(schema.Schema, extProperty.(*openapi_v3.Schema))
				}
			}
			//按照openapi规范，path参数如果有则一定是required
			required = true
		} else if ext = proto.GetExtension(field.Desc.Options(), api.E_Cookie); ext != "" {
			paramName = proto.GetExtension(field.Desc.Options(), api.E_Cookie).(string)
			paramIn = "cookie"
			paramDesc = g.filterCommentString(field.Comments.Leading)
			fieldSchema = g.reflect.schemaOrReferenceForField(field.Desc)
			if schema, ok := fieldSchema.Oneof.(*openapi_v3.SchemaOrReference_Schema); ok {
				// Merge any `Property` annotations with the current
				extProperty := proto.GetExtension(field.Desc.Options(), openapi_v3.E_Property)
				if extProperty != nil {
					proto.Merge(schema.Schema, extProperty.(*openapi_v3.Schema))
				}
			}
		} else if ext = proto.GetExtension(field.Desc.Options(), api.E_Header); ext != "" {
			paramName = proto.GetExtension(field.Desc.Options(), api.E_Header).(string)
			paramIn = "header"
			paramDesc = g.filterCommentString(field.Comments.Leading)
			fieldSchema = g.reflect.schemaOrReferenceForField(field.Desc)
			if schema, ok := fieldSchema.Oneof.(*openapi_v3.SchemaOrReference_Schema); ok {
				// Merge any `Property` annotations with the current
				extProperty := proto.GetExtension(field.Desc.Options(), openapi_v3.E_Property)
				if extProperty != nil {
					proto.Merge(schema.Schema, extProperty.(*openapi_v3.Schema))
				}
			}
		}
		parameter := &openapi_v3.Parameter{
			Name:        paramName,
			In:          paramIn,
			Description: paramDesc,
			Required:    required,
			Schema:      fieldSchema,
		}
		extParameter := proto.GetExtension(field.Desc.Options(), openapi_v3.E_Parameter)
		proto.Merge(parameter, extParameter.(*openapi_v3.Parameter))

		// Append the parameter to the parameters array if it was set
		if paramName != "" && paramIn != "" {
			parameters = append(parameters, &openapi_v3.ParameterOrReference{
				Oneof: &openapi_v3.ParameterOrReference_Parameter{
					Parameter: parameter,
				},
			})
		}

	}

	var RequestBody *openapi_v3.RequestBodyOrReference
	if methodName != "GET" && methodName != "HEAD" && methodName != "DELETE" {
		bodySchema := g.getSchemaByOption(inputMessage, api.E_Body)
		formSchema := g.getSchemaByOption(inputMessage, api.E_Form)
		rawBodySchema := g.getSchemaByOption(inputMessage, api.E_RawBody)

		var additionalProperties []*openapi_v3.NamedMediaType

		if len(bodySchema.Properties.AdditionalProperties) > 0 {
			additionalProperties = append(additionalProperties, &openapi_v3.NamedMediaType{
				Name: "application/json",
				Value: &openapi_v3.MediaType{
					Schema: &openapi_v3.SchemaOrReference{
						Oneof: &openapi_v3.SchemaOrReference_Schema{
							Schema: bodySchema,
						},
					},
				},
			})
		}

		if len(formSchema.Properties.AdditionalProperties) > 0 {
			additionalProperties = append(additionalProperties, &openapi_v3.NamedMediaType{
				Name: "multipart/form-data",
				Value: &openapi_v3.MediaType{
					Schema: &openapi_v3.SchemaOrReference{
						Oneof: &openapi_v3.SchemaOrReference_Schema{
							Schema: formSchema,
						},
					},
				},
			})
		}

		if len(rawBodySchema.Properties.AdditionalProperties) > 0 {
			additionalProperties = append(additionalProperties, &openapi_v3.NamedMediaType{
				Name: "application/octet-stream",
				Value: &openapi_v3.MediaType{
					Schema: &openapi_v3.SchemaOrReference{
						Oneof: &openapi_v3.SchemaOrReference_Schema{
							Schema: rawBodySchema,
						},
					},
				},
			})
		}

		if len(additionalProperties) > 0 {
			RequestBody = &openapi_v3.RequestBodyOrReference{
				Oneof: &openapi_v3.RequestBodyOrReference_RequestBody{
					RequestBody: &openapi_v3.RequestBody{
						//Required: true,
						Content: &openapi_v3.MediaTypes{
							AdditionalProperties: additionalProperties,
						},
					},
				},
			}
		}
	}

	name, header, content := g.getResponseForMessage(d, outputMessage)

	desc := g.filterCommentString(outputMessage.Comments.Leading)
	if desc == "" {
		desc = "Successful response"
	}

	var headerOrEmpty *openapi_v3.HeadersOrReferences

	if len(header.AdditionalProperties) != 0 {
		headerOrEmpty = header
	}

	var contentOrEmpty *openapi_v3.MediaTypes

	if len(content.AdditionalProperties) != 0 {
		contentOrEmpty = content
	}
	var responses *openapi_v3.Responses
	if headerOrEmpty != nil || contentOrEmpty != nil {
		responses = &openapi_v3.Responses{
			ResponseOrReference: []*openapi_v3.NamedResponseOrReference{
				{
					Name: name,
					Value: &openapi_v3.ResponseOrReference{
						Oneof: &openapi_v3.ResponseOrReference_Response{
							Response: &openapi_v3.Response{
								Description: desc,
								Headers:     headerOrEmpty,
								Content:     contentOrEmpty,
							},
						},
					},
				},
			},
		}
	}

	re := regexp.MustCompile(`:(\w+)`)
	path = re.ReplaceAllString(path, `{$1}`)

	op := &openapi_v3.Operation{
		Tags:        []string{tagName},
		Description: description,
		OperationId: operationID,
		Parameters:  parameters,
		Responses:   responses,
		RequestBody: RequestBody,
	}
	if defaultHost != "" {
		op.Servers = append(op.Servers, &openapi_v3.Server{Url: defaultHost})
	}

	return op, path
}

func (g *OpenAPIv3Generator) getResponseForMessage(d *openapi_v3.Document, message *protogen.Message) (string, *openapi_v3.HeadersOrReferences, *openapi_v3.MediaTypes) {

	headers := &openapi_v3.HeadersOrReferences{AdditionalProperties: []*openapi_v3.NamedHeaderOrReference{}}

	//处理api.header
	for _, field := range message.Fields {
		if ext := proto.GetExtension(field.Desc.Options(), api.E_Header); ext != "" {
			headerName := proto.GetExtension(field.Desc.Options(), api.E_Header).(string)
			header := &openapi_v3.Header{
				Description: g.filterCommentString(field.Comments.Leading),
				Schema:      g.reflect.schemaOrReferenceForField(field.Desc),
			}
			headers.AdditionalProperties = append(headers.AdditionalProperties, &openapi_v3.NamedHeaderOrReference{
				Name: headerName,
				Value: &openapi_v3.HeaderOrReference{
					Oneof: &openapi_v3.HeaderOrReference_Header{
						Header: header,
					},
				},
			})
		}
	}

	//处理api.body、api.raw_body
	bodySchema := g.getSchemaByOption(message, api.E_Body)
	rawBodySchema := g.getSchemaByOption(message, api.E_RawBody)

	var additionalProperties []*openapi_v3.NamedMediaType

	if len(bodySchema.Properties.AdditionalProperties) > 0 {
		refSchema := &openapi_v3.NamedSchemaOrReference{
			Name:  g.reflect.formatMessageName(message.Desc),
			Value: &openapi_v3.SchemaOrReference{Oneof: &openapi_v3.SchemaOrReference_Schema{Schema: bodySchema}},
		}
		ref := "#/components/schemas/" + g.reflect.formatMessageName(message.Desc)
		g.addSchemaToDocument(d, refSchema)
		additionalProperties = append(additionalProperties, &openapi_v3.NamedMediaType{
			Name: "application/json",
			Value: &openapi_v3.MediaType{
				Schema: &openapi_v3.SchemaOrReference{
					Oneof: &openapi_v3.SchemaOrReference_Reference{
						Reference: &openapi_v3.Reference{XRef: ref},
					},
				},
			},
		})
	}

	if len(rawBodySchema.Properties.AdditionalProperties) > 0 {
		additionalProperties = append(additionalProperties, &openapi_v3.NamedMediaType{
			Name: "application/octet-stream",
			Value: &openapi_v3.MediaType{
				Schema: &openapi_v3.SchemaOrReference{
					Oneof: &openapi_v3.SchemaOrReference_Schema{
						Schema: rawBodySchema,
					},
				},
			},
		})
	}

	content := &openapi_v3.MediaTypes{
		AdditionalProperties: additionalProperties,
	}

	return "200", headers, content
}

// addOperationToDocument adds an operation to the specified path/method.
func (g *OpenAPIv3Generator) addOperationToDocument(d *openapi_v3.Document, op *openapi_v3.Operation, path string, methodName string) {
	var selectedPathItem *openapi_v3.NamedPathItem
	for _, namedPathItem := range d.Paths.Path {
		if namedPathItem.Name == path {
			selectedPathItem = namedPathItem
			break
		}
	}
	// If we get here, we need to create a path item.
	if selectedPathItem == nil {
		selectedPathItem = &openapi_v3.NamedPathItem{Name: path, Value: &openapi_v3.PathItem{}}
		d.Paths.Path = append(d.Paths.Path, selectedPathItem)
	}
	// Set the operation on the specified method.
	switch methodName {
	case "GET":
		selectedPathItem.Value.Get = op
	case "POST":
		selectedPathItem.Value.Post = op
	case "PUT":
		selectedPathItem.Value.Put = op
	case "DELETE":
		selectedPathItem.Value.Delete = op
	case "PATCH":
		selectedPathItem.Value.Patch = op
	case "OPTIONS":
		selectedPathItem.Value.Options = op
	case "HEAD":
		selectedPathItem.Value.Head = op
	}
}

var (
	HttpMethodOptions = map[*protoimpl.ExtensionInfo]string{
		api.E_Get:     "GET",
		api.E_Post:    "POST",
		api.E_Put:     "PUT",
		api.E_Patch:   "PATCH",
		api.E_Delete:  "DELETE",
		api.E_Options: "OPTIONS",
		api.E_Head:    "HEAD",
	}
)

func getAllOptions(extensions map[*protoimpl.ExtensionInfo]string, opts ...protoreflect.ProtoMessage) map[string]interface{} {
	out := map[string]interface{}{}
	for _, opt := range opts {
		for e, t := range extensions {
			if proto.HasExtension(opt, e) {
				v := proto.GetExtension(opt, e)
				out[t] = v
			}
		}
	}
	return out
}

func (g *OpenAPIv3Generator) addPathsToDocument(d *openapi_v3.Document, services []*protogen.Service) {
	for _, service := range services {
		annotationsCount := 0

		for _, method := range service.Methods {
			comment := g.filterCommentString(method.Comments.Leading)
			inputMessage := method.Input
			outputMessage := method.Output
			operationID := service.GoName + "_" + method.GoName
			rs := getAllOptions(HttpMethodOptions, method.Desc.Options())
			for methodName, path := range rs {
				if methodName != "" {
					annotationsCount++
					var host string
					host = proto.GetExtension(method.Desc.Options(), api.E_Baseurl).(string)

					if host == "" {
						host = proto.GetExtension(service.Desc.Options(), api.E_BaseDomain).(string)
					}
					op, path2 := g.buildOperation(d, methodName, operationID, service.GoName, comment, host, path.(string), inputMessage, outputMessage)
					// Merge any `Operation` annotations with the current
					extOperation := proto.GetExtension(method.Desc.Options(), openapi_v3.E_Operation)

					if extOperation != nil {
						proto.Merge(op, extOperation.(*openapi_v3.Operation))
					}
					g.addOperationToDocument(d, op, path2, methodName)
				}
			}
		}
		if annotationsCount > 0 {
			comment := g.filterCommentString(service.Comments.Leading)
			d.Tags = append(d.Tags, &openapi_v3.Tag{Name: service.GoName, Description: comment})
		}
	}
}

// addSchemaForMessageToDocumentV3 adds the schema to the document if required
func (g *OpenAPIv3Generator) addSchemaToDocument(d *openapi_v3.Document, schema *openapi_v3.NamedSchemaOrReference) {
	if contains(g.generatedSchemas, schema.Name) {
		return
	}
	g.generatedSchemas = append(g.generatedSchemas, schema.Name)
	d.Components.Schemas.AdditionalProperties = append(d.Components.Schemas.AdditionalProperties, schema)
}

// addSchemasForMessagesToDocumentV3 adds info from one file descriptor.
func (g *OpenAPIv3Generator) addSchemasForMessagesToDocumentV3(d *openapi_v3.Document, messages []*protogen.Message) {
	// For each message, generate a definition.
	for _, message := range messages {
		if message.Messages != nil {
			g.addSchemasForMessagesToDocumentV3(d, message.Messages)
		}

		schemaName := g.reflect.formatMessageName(message.Desc)

		// Only generate this if we need it and haven't already generated it.
		if !contains(g.reflect.requiredSchemas, schemaName) ||
			contains(g.generatedSchemas, schemaName) {
			continue
		}

		typeName := g.reflect.fullMessageTypeName(message.Desc)
		messageDescription := g.filterCommentString(message.Comments.Leading)

		// `google.protobuf.Value` and `google.protobuf.Any` have special JSON transcoding
		// so we can't just reflect on the message descriptor.
		if typeName == ".google.protobuf.Value" {
			g.addSchemaToDocument(d, wk.NewGoogleProtobufValueSchema(schemaName))
			continue
		} else if typeName == ".google.protobuf.Any" {
			g.addSchemaToDocument(d, wk.NewGoogleProtobufAnySchema(schemaName))
			continue
		} else if typeName == ".google.rpc.Status" {
			anySchemaName := g.reflect.formatMessageName(anyProtoDesc)
			g.addSchemaToDocument(d, wk.NewGoogleProtobufAnySchema(anySchemaName))
			g.addSchemaToDocument(d, wk.NewGoogleRpcStatusSchema(schemaName, anySchemaName))
			continue
		}

		// Build an array holding the fields of the message.
		definitionProperties := &openapi_v3.Properties{
			AdditionalProperties: make([]*openapi_v3.NamedSchemaOrReference, 0),
		}

		var required []string
		for _, field := range message.Fields {
			// Get the field description from the comments.
			description := g.filterCommentString(field.Comments.Leading)
			// Check the field annotations to see if this is a readonly or writeonly field.
			inputOnly := false
			outputOnly := false
			extension := proto.GetExtension(field.Desc.Options(), annotations.E_FieldBehavior)
			if extension != nil {
				switch v := extension.(type) {
				case []annotations.FieldBehavior:
					for _, vv := range v {
						switch vv {
						case annotations.FieldBehavior_OUTPUT_ONLY:
							outputOnly = true
						case annotations.FieldBehavior_INPUT_ONLY:
							inputOnly = true
						case annotations.FieldBehavior_REQUIRED:
							required = append(required, g.reflect.formatFieldName(field.Desc))
						}
					}
				default:
					log.Printf("unsupported extension type %T", extension)
				}
			}

			// The field is either described by a reference or a schema.
			fieldSchema := g.reflect.schemaOrReferenceForField(field.Desc)
			if fieldSchema == nil {
				continue
			}

			// If this field has siblings and is a $ref now, create a new schema use `allOf` to wrap it
			wrapperNeeded := inputOnly || outputOnly || description != ""
			if wrapperNeeded {
				if _, ok := fieldSchema.Oneof.(*openapi_v3.SchemaOrReference_Reference); ok {
					fieldSchema = &openapi_v3.SchemaOrReference{Oneof: &openapi_v3.SchemaOrReference_Schema{Schema: &openapi_v3.Schema{
						AllOf: []*openapi_v3.SchemaOrReference{fieldSchema},
					}}}
				}
			}

			if schema, ok := fieldSchema.Oneof.(*openapi_v3.SchemaOrReference_Schema); ok {
				schema.Schema.Description = description
				schema.Schema.ReadOnly = outputOnly
				schema.Schema.WriteOnly = inputOnly

				// Merge any `Property` annotations with the current
				extProperty := proto.GetExtension(field.Desc.Options(), openapi_v3.E_Property)
				if extProperty != nil {
					proto.Merge(schema.Schema, extProperty.(*openapi_v3.Schema))
				}
			}

			definitionProperties.AdditionalProperties = append(
				definitionProperties.AdditionalProperties,
				&openapi_v3.NamedSchemaOrReference{
					Name:  g.reflect.formatFieldName(field.Desc),
					Value: fieldSchema,
				},
			)
		}

		schema := &openapi_v3.Schema{
			Type:        "object",
			Description: messageDescription,
			Properties:  definitionProperties,
			Required:    required,
		}

		// Merge any `Schema` annotations with the current
		extSchema := proto.GetExtension(message.Desc.Options(), openapi_v3.E_Schema)
		if extSchema != nil {
			proto.Merge(schema, extSchema.(*openapi_v3.Schema))
		}

		// Add the schema to the components.schema list.
		g.addSchemaToDocument(d, &openapi_v3.NamedSchemaOrReference{
			Name: schemaName,
			Value: &openapi_v3.SchemaOrReference{
				Oneof: &openapi_v3.SchemaOrReference_Schema{
					Schema: schema,
				},
			},
		})
	}
}
