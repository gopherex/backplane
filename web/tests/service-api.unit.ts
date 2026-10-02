import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import { create } from '@bufbuild/protobuf';
import { ManifestSchema, RouteSchema, RouteKind } from '@gopherex/backplane-api/backplanepb/v1/manifest_pb';
import { readAPIDocument, type APIDocument } from '../packages/platform-ui/src/api-document';
import { externalAPIManifest } from '../apps/catalog/src/api-fixture';
import { bundleOpenAPI } from '../packages/platform-ui/src/api-bundle';

const manifest = externalAPIManifest();
function document(index: number): APIDocument {
  const result = readAPIDocument(manifest.routes[index]!, manifest);
  if (!('document' in result)) throw new Error(result.detail ?? result.problem);
  return result.document;
}
function openAPI(value: unknown) {
  const route = create(RouteSchema, { kind: RouteKind.HTTP, schema: { case: 'openapi', value: new TextEncoder().encode(typeof value === 'string' ? value : JSON.stringify(value)) } });
  return readAPIDocument(route, create(ManifestSchema));
}

describe('external service API documents', () => {
  it('resolves relative files and cyclic cross-file references once each', async () => {
    const files: Record<string, unknown> = {
      'api/root.yaml': { openapi: '3.0.3', paths: { '/x': { $ref: '../paths/x.yaml' } }, components: { schemas: { Visitor: { $ref: '../types/visitor.yaml' } } } },
      'paths/x.yaml': { get: { responses: { '200': { content: { 'application/json': { schema: { $ref: '../types/visitor.yaml' } } } } } } },
      'types/visitor.yaml': { type: 'object', properties: { child: { $ref: './visitor.yaml' } } },
    };
    const calls: string[] = [];
    const source = await bundleOpenAPI('api/root.yaml', async (file) => { calls.push(file); return new TextEncoder().encode(JSON.stringify(files[file])); });
    const result = openAPI(source);
    expect(calls).toEqual(['api/root.yaml', 'paths/x.yaml', 'types/visitor.yaml']);
    expect('document' in result && result.document.operations[0]?.name).toBe('/x');
    expect('document' in result && result.document.warnings).toEqual([]);
  });
  it.each(['https://outside.example/api.json', '//outside.example/api.json', '../../outside.yaml', '/etc/passwd'])('refuses references outside the bundle: %s', async (ref) => {
    const calls: string[] = [];
    await expect(bundleOpenAPI('api.yaml', async (file) => { calls.push(file); return new TextEncoder().encode(JSON.stringify({ openapi: '3.0.3', paths: { '/x': { $ref: ref } } })); })).rejects.toThrow();
    expect(calls).toEqual(['api.yaml']);
  });
  it('resolves shared path parameters and keeps recursive schemas finite', () => {
    const doc = document(0);
    expect(doc.operations.map((op) => op.method)).toEqual(['GET', 'DELETE']);
    expect(doc.operations[0]?.sections[0]?.fields).toEqual([{ name: 'id (path)', type: 'string', required: true, description: '' }]);
    expect(doc.schemas[0]?.fields?.find((field) => field.name === 'friend')?.type).toBe('Visitor');
    expect(doc.warnings).toEqual([]);
  });
  it('lets operation-level parameters override path-level parameters', () => {
    const result = openAPI({ openapi: '3.1.0', paths: { '/x': { parameters: [{ name: 'q', in: 'query', schema: { type: 'string' } }], get: { parameters: [{ name: 'q', in: 'query', required: true, schema: { type: 'integer' } }] } } } });
    expect('document' in result && result.document.operations[0]?.sections[0]?.fields).toEqual([{ name: 'q (query)', type: 'integer', required: true, description: '' }]);
  });
  it('reads the real embedded multi-file hello API', async () => {
    const source = await bundleOpenAPI('openapi.yaml', async (file) => new Uint8Array(readFileSync(new URL(`../../examples/hello/internal/web/${file}`, import.meta.url))));
    const result = openAPI(source);
    expect('document' in result && result.document.operations[0]?.name).toBe('/hello/');
    expect('document' in result && result.document.warnings).toEqual([]);
  });
  it('supports Swagger 2.0 definitions and responses', () => {
    const result = openAPI({ swagger: '2.0', paths: { '/x': { get: { responses: { '200': { $ref: '#/responses/OK' } } } } }, responses: { OK: { description: 'Good' } }, definitions: { Result: { type: 'string' } } });
    expect('document' in result && result.document.operations[0]?.sections[0]?.description).toBe('Good');
    expect('document' in result && result.document.schemas[0]?.name).toBe('Result');
  });
  it('reports external, missing and cyclic reference chains without fetching them', () => {
    const result = openAPI({ openapi: '3.0.3', paths: { '/x': { $ref: './paths.yaml#/x' }, '/y': { $ref: '#/missing' }, '/z': { $ref: '#/paths/~1z' } } });
    expect('document' in result && result.document.warnings).toEqual(['./paths.yaml#/x', '#/missing', '#/paths/~1z']);
  });
  it('decodes escaped JSON pointer tokens', () => {
    const result = openAPI({ openapi: '3.0.3', paths: { '/x': { $ref: '#/components/pathItems/a~1b~0c' } }, components: { pathItems: { 'a/b~c': { get: { summary: 'Resolved' } } } } });
    expect('document' in result && result.document.operations[0]?.title).toBe('Resolved');
  });
  it('uses shared descriptors and filters out internal RPC services', () => {
    const doc = document(1);
    expect(doc.operations.map((op) => op.name)).toEqual(['/public.v1.Greeter/Watch']);
    expect(doc.operations[0]?.sections[1]?.fields).toEqual([{ name: 'stream', type: 'public.v1.Reply' }]);
    expect(doc.schemas.map((schema) => schema.name)).toEqual(['public.v1.Request', 'public.v1.Reply']);
  });
  it('uses route-local descriptor sets for ws-proto', () => {
    expect(document(2).operations).toEqual(document(1).operations);
  });
  it('renders GraphQL root operations and required argument types', () => {
    const doc = document(3);
    expect(doc.operations[0]?.method).toBe('QUERY');
    expect(doc.operations[0]?.sections[0]?.fields).toEqual([{ name: 'name', type: 'String!', required: true, description: '' }]);
    const route = create(RouteSchema, { kind: RouteKind.GRAPHQL, schema: { case: 'graphql', value: new TextEncoder().encode(JSON.stringify(JSON.parse(doc.source).data)) } });
    expect(readAPIDocument(route, manifest)).toMatchObject({ document: { operations: doc.operations } });
  });
  it('distinguishes missing documents from malformed documents', () => {
    expect(readAPIDocument(manifest.routes[4]!, manifest)).toEqual({ problem: 'missing' });
    expect(readAPIDocument(manifest.routes[5]!, manifest)).toMatchObject({ problem: 'invalid' });
    expect(openAPI('openapi: 3.0.3\npaths: [')).toMatchObject({ problem: 'invalid' });
    expect(openAPI('openapi: 3.0.3\npaths: &paths { self: *paths }')).toMatchObject({ problem: 'invalid' });
  });
});
