import type { MeField } from "../api/client";
import { Checkbox, Field, Input } from "../ui";

// A field answer as the form holds it: a string for a text field, a boolean for a checkbox.
export type FieldValue = string | boolean;
export type FieldValues = Record<number, FieldValue>;

// initialFieldValues seeds the form from the field definitions and any answers already on file.
// A text field with no answer is the empty string; a checkbox defaults to unchecked.
export function initialFieldValues(fields: readonly MeField[]): FieldValues {
  const out: FieldValues = {};
  for (const f of fields) {
    if (f.field_type === "boolean") {
      out[f.id] = f.value === true;
    } else {
      out[f.id] = typeof f.value === "string" ? f.value : "";
    }
  }
  return out;
}

// answersFor turns the form state into the wire payload, restricted to the fields whose ids are
// passed. The caller decides which fields it may write (all of them at registration; only the
// editable-or-owed ones on /me), so this stays a pure projection.
export function answersFor(
  ids: readonly number[],
  values: FieldValues,
): { field_id: number; value: FieldValue }[] {
  return ids.map((id) => ({ field_id: id, value: values[id] ?? "" }));
}

interface CustomFieldInputsProps {
  fields: readonly MeField[];
  values: FieldValues;
  onChange: (id: number, value: FieldValue) => void;
  // disabled marks a field the caller renders read-only (a non-editable, already-answered field on
  // the /me editor). It is never used at registration, where every field is answerable.
  disabled?: (field: MeField) => boolean;
  errors?: Record<number, string>;
}

/** Renders one input per custom field, typed to the field. Shared by the register and /me forms. */
export function CustomFieldInputs({
  fields,
  values,
  onChange,
  disabled,
  errors,
}: CustomFieldInputsProps) {
  return (
    <>
      {fields.map((f) => {
        const isDisabled = disabled?.(f) ?? false;
        if (f.field_type === "boolean") {
          return (
            <Field key={f.id} name={`field-${f.id}`} label={f.name} error={errors?.[f.id]}>
              <Checkbox
                checked={values[f.id] === true}
                disabled={isDisabled}
                label={f.description ?? f.name}
                onChange={(e) => onChange(f.id, e.target.checked)}
              />
            </Field>
          );
        }
        return (
          <Field
            key={f.id}
            name={`field-${f.id}`}
            label={f.name}
            required={f.required}
            hint={f.description ?? undefined}
            error={errors?.[f.id]}
          >
            <Input
              value={typeof values[f.id] === "string" ? (values[f.id] as string) : ""}
              maxLength={500}
              disabled={isDisabled}
              onChange={(e) => onChange(f.id, e.target.value)}
            />
          </Field>
        );
      })}
    </>
  );
}
