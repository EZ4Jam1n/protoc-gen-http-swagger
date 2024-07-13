# protoc-gen-http-swagger

English | [中文](README_CN.md)

HTTP Swagger document generation plugin for cloudwego/cwgo & hertz.

## Supported hz Annotations

### Request Specification

1. Interface request fields need to be associated with a certain type of HTTP parameter and parameter name using annotations. Fields without annotations will not be processed.
2. Generate the parameters and requestBody of the operation in Swagger according to the request message in the method.
3. If the HTTP request uses the `GET`, `HEAD`, or `DELETE` methods, the `api.body` annotation in the `request` definition is invalid, and only `api.query`, `api.path`, `api.cookie`, `api.header` are valid.

#### Annotation Explanation

| Annotation     | Explanation                                                                 |  
|----------------|-----------------------------------------------------------------------------|
| `api.query`    | `api.query` corresponds to parameter in: query                              |  
| `api.path`     | `api.path` corresponds to parameter in: path, required is true              |
| `api.header`   | `api.header` corresponds to parameter in: header                            |       
| `api.cookie`   | `api.cookie` corresponds to parameter in: cookie                            |
| `api.body`     | `api.body` corresponds to requestBody content: application/json             | 
| `api.form`     | `api.form` corresponds to requestBody content: multipart/form-data          | 
| `api.raw_body` | `api.raw_body` corresponds to requestBody content: application/octet-stream | 

### Response Specification

1. Interface response fields need to be associated with a certain type of HTTP parameter and parameter name using annotations. Fields without annotations will not be processed.
2. Generate the responses of the operation in Swagger according to the response message in the method.

#### Annotation Explanation

| Annotation     | Explanation                                                              |  
|----------------|--------------------------------------------------------------------------|
| `api.header`   | `api.header` corresponds to response header                              |
| `api.body`     | `api.body` corresponds to response content: application/json             |
| `api.raw_body` | `api.raw_body` corresponds to response content: application/octet-stream |

### Method Specification

1. Each method is associated with a pathItem through an annotation.

#### Annotation Explanation

| Annotation    | Explanation                                                 |  
|---------------|-------------------------------------------------------------|
| `api.get`     | `api.get` corresponds to GET request, only parameters       |
| `api.put`     | `api.put` corresponds to PUT request                        |
| `api.post`    | `api.post` corresponds to POST request                      |
| `api.patch`   | `api.patch` corresponds to PATCH request                    |
| `api.delete`  | `api.delete` corresponds to DELETE request, only parameters |
| `api.options` | `api.options` corresponds to OPTIONS request                |
| `api.head`    | `api.head` corresponds to HEAD request, only parameters     |
| `api.baseurl` | `api.baseurl` corresponds to server URL of pathItem         |

### Service Specification

#### Annotation Explanation

| Annotation        | Explanation                                 |  
|-------------------|---------------------------------------------|
| `api.base_domain` | `api.base_domain` corresponds to server URL |

## openapi Annotations

| Annotation          | Component | Explanation                                               |  
|---------------------|-----------|-----------------------------------------------------------|
| `openapi.operation` | Method    | Used to supplement the operation of pathItem              |
| `openapi.property`  | Field     | Used to supplement the property of schema                 |
| `openapi.schema`    | Message   | Used to supplement the schema of requestBody and response |
| `openapi.document`  | Document  | Used to supplement the Swagger document                   |
| `openapi.parameter` | Field     | Used to supplement the parameter                          |

For more usage, please refer to [Example](./test/idl/hello.proto).

## Installation

```sh
# Clone the official repository
git clone https://github.com/hertz-contrib/swagger-generate
cd swagger-generate

# Initialize and update submodules
git submodule update --init --recursive

# Navigate to the submodule directory and install the plugin
cd hertz/protoc-gen-http-swagger
go install

# Verify the installation
protoc-gen-http-swagger --version
```

## More info

See [examples](./test/idl/hello.proto)