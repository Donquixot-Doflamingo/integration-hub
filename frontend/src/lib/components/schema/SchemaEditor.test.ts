import { fireEvent, render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import SchemaEditor from './SchemaEditor.svelte';

// HUB-436 (ADR-0023): the schema registry editor: sample -> fields, required toggling, field
// edits, JSON Schema view both ways, optional parts that emit null when empty.

type Doc = Record<string, unknown> | null;

function setup(props: { value?: Doc; optional?: boolean; label?: string; noun?: string } = {}) {
	const onchange = vi.fn<(d: Doc) => void>();
	render(SchemaEditor, {
		props: { value: props.value ?? null, label: props.label ?? 'Body', onchange, ...props }
	});
	const last = () => onchange.mock.calls.at(-1)?.[0];
	return { onchange, last, user: userEvent.setup() };
}

const AGE_BODY = {
	type: 'object',
	required: ['firstName', 'lastName'],
	properties: { firstName: { type: 'string', minLength: 1 }, lastName: { type: 'string' } }
};

describe('SchemaEditor', () => {
	it('shows an existing schema as fields with required/optional, without emitting', () => {
		const { onchange } = setup({
			value: { ...AGE_BODY, required: ['firstName'] }
		});
		expect(screen.getByRole('group', { name: /^Body/ })).toHaveTextContent('2 fields');
		expect(screen.getByLabelText('Name of firstName')).toHaveValue('firstName');
		expect(screen.getByRole('checkbox', { name: 'firstName required' })).toBeChecked();
		expect(screen.getByRole('checkbox', { name: 'lastName required' })).not.toBeChecked();
		expect(screen.getByLabelText('Type of firstName')).toHaveValue('string');
		// Keywords without a column are listed on their field.
		expect(screen.getByText('minLength 1')).toBeInTheDocument();
		expect(onchange).not.toHaveBeenCalled();
	});

	it('generates fields from a pasted sample, nested objects and arrays included', async () => {
		const { user, last } = setup();
		await user.click(screen.getByRole('button', { name: 'Paste a sample' }));
		await fireEvent.input(screen.getByLabelText('Sample body (JSON)'), {
			target: { value: '{"firstName": "Mich", "age": 57, "user": {"id": "u_1"}, "tags": ["a"]}' }
		});
		await user.click(screen.getByRole('button', { name: 'Generate fields' }));
		expect(last()).toEqual({
			type: 'object',
			required: ['firstName', 'age', 'user', 'tags'],
			properties: {
				firstName: { type: 'string' },
				age: { type: 'integer' },
				user: { type: 'object', required: ['id'], properties: { id: { type: 'string' } } },
				tags: { type: 'array', items: { type: 'string' } }
			}
		});
		expect(screen.getByLabelText('Name of user.id')).toBeInTheDocument();
		expect(screen.getByLabelText('Type of tags[]')).toHaveValue('string');
		expect(screen.getByRole('status')).toHaveTextContent(/Generated 5 fields/);
	});

	it('refuses a sample that is not a JSON object, with the reason', async () => {
		const { user, onchange } = setup();
		await user.click(screen.getByRole('button', { name: 'Paste a sample' }));
		const sample = screen.getByLabelText('Sample body (JSON)');
		await fireEvent.input(sample, { target: { value: '{"a": ' } });
		await user.click(screen.getByRole('button', { name: 'Generate fields' }));
		expect(sample).toHaveAttribute('aria-invalid', 'true');
		expect(sample).toHaveAccessibleDescription(/not valid JSON/);
		expect(onchange).not.toHaveBeenCalled();
	});

	it('ticks a field optional and back, writing `required`', async () => {
		const { user, last } = setup({ value: AGE_BODY });
		await user.click(screen.getByRole('checkbox', { name: 'lastName required' }));
		expect(last()).toMatchObject({ required: ['firstName'] });
		await user.click(screen.getByRole('checkbox', { name: 'firstName required' }));
		expect(last() && 'required' in last()!).toBe(false);
		await user.click(screen.getByRole('checkbox', { name: 'lastName required' }));
		expect(last()).toMatchObject({ required: ['lastName'] });
	});

	it('adds, renames, retypes, describes and removes fields by keyboard', async () => {
		const { user, last } = setup({ value: AGE_BODY, noun: 'field' });
		await user.click(screen.getByRole('button', { name: '+ Add field' }));
		const name = screen.getByLabelText('Name of field');
		await user.clear(name);
		await user.type(name, 'address');
		await user.selectOptions(screen.getByLabelText('Type of address'), 'object');
		await user.click(screen.getByRole('button', { name: 'Add a field inside address' }));
		await user.type(screen.getByLabelText('Description of address.field'), 'Postal code');
		expect(last()).toMatchObject({
			properties: {
				address: {
					type: 'object',
					properties: { field: { type: 'string', description: 'Postal code' } }
				}
			}
		});
		await user.click(screen.getByRole('button', { name: 'Remove address' }));
		expect(Object.keys((last() as { properties: object }).properties)).toEqual([
			'firstName',
			'lastName'
		]);
		expect(screen.getByRole('status')).toHaveTextContent('Removed address.');
	});

	it('flags empty and duplicate names on the field', async () => {
		const { user } = setup({ value: AGE_BODY });
		const name = screen.getByLabelText('Name of lastName');
		await user.clear(name);
		await user.type(name, 'firstName');
		expect(screen.getByRole('alert')).toHaveTextContent(
			'firstName: "firstName" is declared twice.'
		);
		expect(screen.getAllByLabelText('Name of firstName')[1]).toHaveAttribute(
			'aria-invalid',
			'true'
		);
	});

	it('round-trips through the JSON Schema view; invalid JSON keeps the operator there', async () => {
		const { user, last, onchange } = setup({ value: AGE_BODY });
		await user.click(screen.getByRole('button', { name: 'Edit as JSON Schema' }));
		const text = screen.getByLabelText('Body as JSON Schema') as HTMLTextAreaElement;
		expect(JSON.parse(text.value)).toEqual(AGE_BODY);

		await fireEvent.input(text, { target: { value: '{"type": "object", "properties": ' } });
		expect(text).toHaveAttribute('aria-invalid', 'true');
		expect(onchange).not.toHaveBeenCalled();
		await user.click(screen.getByRole('button', { name: 'Fields' }));
		expect(screen.getByLabelText('Body as JSON Schema')).toBeInTheDocument();

		await fireEvent.input(text, { target: { value: '{"type": "objekt"}' } });
		expect(text).toHaveAccessibleDescription(/"type" must be one of/);

		const edited = {
			type: 'object',
			required: ['name'],
			properties: { name: { type: 'string', pattern: '^[a-z]+$' } }
		};
		await fireEvent.input(text, { target: { value: JSON.stringify(edited) } });
		expect(last()).toEqual(edited);
		await user.click(screen.getByRole('button', { name: 'Fields' }));
		expect(screen.getByRole('checkbox', { name: 'name required' })).toBeChecked();
		expect(screen.getByText('pattern ^[a-z]+$')).toBeInTheDocument();
	});

	it('an optional part emits null when its last field goes (not sent)', async () => {
		const { user, last } = setup({
			label: 'Headers',
			optional: true,
			noun: 'header',
			value: { type: 'object', properties: { 'X-Request-Source': { type: 'string' } } }
		});
		expect(
			screen.getByRole('group', { name: /Headers \(optional: empty = not sent\)/ })
		).toBeVisible();
		await user.click(screen.getByRole('button', { name: 'Remove X-Request-Source' }));
		expect(last()).toBeNull();
		expect(screen.getByText(/No headers: nothing is sent/)).toBeInTheDocument();
		await user.click(screen.getByRole('button', { name: '+ Add header' }));
		expect(last()).toEqual({ type: 'object', properties: { header: { type: 'string' } } });
	});

	it('a required part emits {type: object} when empty', async () => {
		const { user, last } = setup({
			value: { type: 'object', properties: { a: { type: 'string' } } }
		});
		await user.click(screen.getByRole('button', { name: 'Remove a' }));
		expect(last()).toEqual({ type: 'object' });
	});

	it('shows a newer document from the parent', async () => {
		const onchange = vi.fn();
		const { rerender } = render(SchemaEditor, {
			props: { value: AGE_BODY, label: 'Body', onchange }
		});
		await rerender({ value: { type: 'object', properties: { city: { type: 'string' } } } });
		const table = screen.getByRole('region', { name: 'Body fields' });
		expect(within(table).getByLabelText('Name of city')).toBeInTheDocument();
		expect(within(table).queryByLabelText('Name of firstName')).toBeNull();
	});
});
