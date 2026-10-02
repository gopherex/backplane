import { fromBinary, toJson } from '@bufbuild/protobuf';
import { FileDescriptorSetSchema, type DescriptorProto, type FieldDescriptorProto } from '@bufbuild/protobuf/wkt';
import { parseDocument } from 'yaml';
import { RouteKind, type Manifest, type Route } from '@gopherex/backplane-api/backplanepb/v1/manifest_pb';

type ObjectValue = Record<string, unknown>;
export interface APIField { name: string; type: string; required?: boolean; description?: string }
export interface APISection { name: string; description?: string; fields?: APIField[]; value?: unknown }
export interface APIOperation { id: string; method: string; name: string; title?: string; description?: string; deprecated?: boolean; sections: APISection[] }
export interface APIDocument { title: string; description?: string; format: string; operations: APIOperation[]; schemas: APISection[]; metadata?: unknown; source: string; warnings: string[] }
export type APIResult = { document: APIDocument } | { problem: 'missing' | 'invalid'; detail?: string };
const object = (value: unknown): ObjectValue => value !== null && typeof value === 'object' && !Array.isArray(value) ? value as ObjectValue : {};
const list = (value: unknown): unknown[] => Array.isArray(value) ? value : [];
const string = (value: unknown): string => typeof value === 'string' ? value : '';
const entries = (value: unknown) => Object.entries(object(value));
const decode = (bytes: Uint8Array) => new TextDecoder('utf-8', { fatal: true }).decode(bytes);

/** The catalog carries self-contained documents. Never fetch a schema's URLs. */
export function readAPIDocument(route: Route, manifest: Manifest): APIResult {
  try {
    if (route.schema.case === 'openapi') return { document: openAPI(decode(route.schema.value)) };
    if (route.schema.case === 'graphql') return { document: graphQL(decode(route.schema.value)) };
    const bytes = route.schema.case === 'descriptors' ? route.schema.value : manifest.descriptors;
    if ([RouteKind.GRPC, RouteKind.CONNECT, RouteKind.WS_PROTO].includes(route.kind) && bytes.length) {
      return { document: protobuf(bytes, route.services) };
    }
    return { problem: 'missing' };
  } catch (error) { return { problem: 'invalid', detail: error instanceof Error ? error.message : String(error) }; }
}

function openAPI(source: string): APIDocument {
  const parsed = parseDocument(source, { uniqueKeys: true });
  if (parsed.errors.length) throw parsed.errors[0];
  const root = object(parsed.toJS({ maxAliasCount: 50 }));
  const version = string(root.openapi) || string(root.swagger);
  if (!/^3\.(0|1|2)\./.test(version) && version !== '2.0') throw new Error('Expected OpenAPI 3.x or Swagger 2.0.');
  const warnings = new Set<string>();
  const resolve = (value: unknown): ObjectValue => {
    const seen = new Set<string>();
    let current = object(value);
    while (typeof current.$ref === 'string') {
      const ref = current.$ref;
      if (seen.has(ref)) { warnings.add(ref); break; }
      seen.add(ref);
      if (ref !== '#' && !ref.startsWith('#/')) { warnings.add(ref); break; }
      let target: unknown = root;
      for (const part of ref === '#' ? [] : decodeURIComponent(ref.slice(2)).split('/')) {
        const key = part.replace(/~1/g, '/').replace(/~0/g, '~');
        target = Object.hasOwn(object(target), key) ? object(target)[key] : undefined;
      }
      if (target === undefined) { warnings.add(ref); break; }
      const { $ref: _, ...siblings } = current;
      current = { ...object(target), ...siblings };
    }
    return current;
  };
  // Check references without expanding schemas (recursive models remain finite).
  const scan = (value: unknown, ancestors = new Set<unknown>()) => {
    if (!value || typeof value !== 'object' || ancestors.has(value)) return;
    const next = new Set(ancestors); next.add(value);
    if (object(value).$ref) resolve(value);
    for (const child of Object.values(value)) scan(child, next);
  };
  scan(root);
  // YAML object aliases are not an OpenAPI reference mechanism. Reject cycles.
  const normalizedSource = JSON.stringify(root, null, 2);
  const mediaFields = (content: unknown): APIField[] => entries(content).map(([name, media]) => ({ name, type: schemaType(object(media).schema) }));
  const operations: APIOperation[] = [];
  for (const [path, value] of entries(root.paths)) {
    if (!path.startsWith('/')) continue;
    const pathItem = resolve(value);
    for (const method of ['get', 'post', 'put', 'patch', 'delete', 'head', 'options', 'trace']) {
      if (!pathItem[method]) continue;
      const op = resolve(pathItem[method]);
      const parameters = new Map<string, ObjectValue>();
      for (const parameter of [...list(pathItem.parameters), ...list(op.parameters)]) {
        const param = resolve(parameter); parameters.set(`${param.in}:${param.name}`, param);
      }
      const sections: APISection[] = [];
      if (parameters.size) sections.push({ name: 'parameters', fields: [...parameters.values()].map((param) => ({
        name: `${string(param.name)} (${string(param.in)})`, type: schemaType(param.schema ?? param), required: param.required === true || param.in === 'path', description: string(param.description),
      })), value: [...parameters.values()] });
      if (op.requestBody) {
        const body = resolve(op.requestBody);
        sections.push({ name: 'requestBody', description: string(body.description), fields: mediaFields(body.content).map((field) => ({ ...field, required: body.required === true })), value: body });
      }
      for (const [code, response] of entries(op.responses)) {
        const result = resolve(response);
        sections.push({ name: code, description: string(result.description), fields: result.content ? mediaFields(result.content) : result.schema ? [{ name: 'body', type: schemaType(result.schema) }] : [], value: result });
      }
      if (op.security !== undefined || root.security !== undefined) sections.push({ name: 'security', value: op.security ?? root.security });
      operations.push({ id: `${method}:${path}`, method: method.toUpperCase(), name: path, title: string(op.summary) || string(op.operationId),
        description: string(op.description), deprecated: op.deprecated === true, sections });
    }
  }
  const info = object(root.info);
  const schemaEntries = entries(object(root.components).schemas ?? root.definitions);
  for (const [key, documents] of entries(root)) if (key.startsWith('x-backplane-documents')) {
    for (const [file, value] of entries(documents)) {
      const schema = object(value);
      if (schema.type || schema.properties || schema.enum || schema.allOf || schema.oneOf || schema.anyOf) schemaEntries.push([file, value]);
    }
  }
  return { title: string(info.title), description: string(info.description), format: `OpenAPI ${version}`, operations,
    schemas: schemaEntries.map(([name, value]) => {
      const schema = resolve(value), required = list(schema.required);
      return { name, description: string(schema.description), fields: entries(schema.properties).map(([name, field]) => ({ name, type: schemaType(field), required: required.includes(name), description: string(resolve(field).description) })), value: schema };
    }), metadata: { servers: root.servers, host: root.host, basePath: root.basePath, schemes: root.schemes,
      securitySchemes: object(root.components).securitySchemes ?? root.securityDefinitions }, source: normalizedSource, warnings: [...warnings] };
}

function schemaType(value: unknown, depth = 0): string {
  if (depth > 12) return '…';
  const schema = object(value);
  if (typeof schema.$ref === 'string') return (schema.$ref.split('/').at(-1) || schema.$ref).replace(/~1/g, '/').replace(/~0/g, '~');
  if (schema.type === 'array') return `${schemaType(schema.items, depth + 1)}[]`;
  if (schema.oneOf || schema.anyOf || schema.allOf) return list(schema.oneOf ?? schema.anyOf ?? schema.allOf).map((item) => schemaType(item, depth + 1)).join(schema.allOf ? ' & ' : ' | ');
  return (Array.isArray(schema.type) ? schema.type.join(' | ') : string(schema.type) || 'any') + (schema.format ? ` (${schema.format})` : '') + (schema.nullable ? ' | null' : '');
}

function protobuf(bytes: Uint8Array, names: string[]): APIDocument {
  const set = fromBinary(FileDescriptorSetSchema, bytes), operations: APIOperation[] = [], schemas: APISection[] = [];
  if (!set.file.length) throw new Error('The descriptor set contains no files.');
  const scalar = ['', 'double', 'float', 'int64', 'uint64', 'int32', 'fixed64', 'fixed32', 'bool', 'string', 'group', 'message', 'bytes', 'uint32', 'enum', 'sfixed32', 'sfixed64', 'sint32', 'sint64'];
  const fieldType = (field: FieldDescriptorProto) => (field.typeName?.replace(/^\./, '') || scalar[field.type ?? 0] || '?') + (field.label === 3 ? '[]' : '');
  for (const file of set.file) {
    const comments = (path: number[]) => file.sourceCodeInfo?.location.find((entry) => entry.path.join('.') === path.join('.'))?.leadingComments?.trim();
    const qualify = (name: string) => [file.package, name].filter(Boolean).join('.');
    const messages = (items: DescriptorProto[], prefix: string, path: number[]) => {
      items.forEach((message, index) => {
        const name = `${prefix}${message.name}`, at = [...path, index];
        schemas.push({ name, description: comments(at), fields: message.field.map((field, index) => ({
          name: field.name ?? '', type: fieldType(field), required: field.label === 2, description: comments([...at, 2, index]),
        })) });
        messages(message.nestedType, `${name}.`, [...at, 3]);
        for (const entry of message.enumType) schemas.push({ name: `${name}.${entry.name}`, fields: entry.value.map((item) => ({ name: item.name ?? '', type: String(item.number) })) });
      });
    };
    messages(file.messageType, file.package ? `${file.package}.` : '', [4]);
    for (const entry of file.enumType) schemas.push({ name: qualify(entry.name ?? ''), fields: entry.value.map((item) => ({ name: item.name ?? '', type: String(item.number) })) });
    file.service.forEach((service, serviceIndex) => {
      const name = qualify(service.name ?? '');
      if (names.length && !names.includes(name)) return;
      service.method.forEach((method, methodIndex) => operations.push({ id: `/${name}/${method.name}`, method: 'RPC', name: `/${name}/${method.name}`,
        description: comments([6, serviceIndex, 2, methodIndex]), deprecated: method.options?.deprecated,
        sections: [{ name: 'requestBody', fields: [{ name: method.clientStreaming ? 'stream' : 'message', type: method.inputType?.replace(/^\./, '') ?? '' }] },
          { name: 'response', fields: [{ name: method.serverStreaming ? 'stream' : 'message', type: method.outputType?.replace(/^\./, '') ?? '' }] }],
      }));
    });
  }
  return { title: names.join(', '), format: 'Protobuf', operations, schemas, source: JSON.stringify(toJson(FileDescriptorSetSchema, set), null, 2), warnings: [] };
}

function graphQL(source: string): APIDocument {
  const root = object(JSON.parse(source)), schema = object(root.__schema ?? object(root.data).__schema);
  if (!Array.isArray(schema.types)) throw new Error('Expected a GraphQL introspection result with __schema.types.');
  const types = schema.types.map(object);
  const typeName = (value: unknown, depth = 0): string => {
    if (depth > 12) return '…';
    const ref = object(value);
    return ref.kind === 'NON_NULL' ? `${typeName(ref.ofType, depth + 1)}!` : ref.kind === 'LIST' ? `[${typeName(ref.ofType, depth + 1)}]` : string(ref.name);
  };
  const fields = (value: unknown): APIField[] => list(value).map(object).map((field) => ({
    name: string(field.name), type: typeName(field.type), required: object(field.type).kind === 'NON_NULL', description: string(field.description),
  }));
  const operations: APIOperation[] = [];
  for (const kind of ['query', 'mutation', 'subscription']) {
    const rootName = string(object(schema[`${kind}Type`]).name), type = types.find((entry) => entry.name === rootName);
    for (const field of list(type?.fields).map(object)) operations.push({ id: `${kind}:${field.name}`, method: kind.toUpperCase(), name: string(field.name),
      description: string(field.description), deprecated: field.isDeprecated === true,
      sections: [{ name: 'parameters', fields: fields(field.args) }, { name: 'response', fields: [{ name: string(field.name), type: typeName(field.type) }] }],
    });
  }
  return { title: 'GraphQL', format: 'GraphQL', operations, schemas: types.filter((type) => !string(type.name).startsWith('__')).map((type) => ({
    name: string(type.name), description: string(type.description), fields: fields(type.fields ?? type.inputFields), value: type,
  })), source: JSON.stringify(root, null, 2), warnings: [] };
}
