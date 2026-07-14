import { isApiError } from "../api/client";
import { fieldErrors, type FieldErrors } from "../ui";

/** The server's own words, which are always more specific than anything we could invent. */
export function errorDetail(error: unknown): string {
  if (isApiError(error)) return error.detail;
  if (error instanceof Error) return error.message;
  return "something went wrong";
}

/**
 * Per-field messages from a 422, keyed the way `<Field name>` is.
 *
 * `ApiError` has already parsed the problem document, so re-wrap its list into the shape the
 * form primitive's parser expects rather than reimplementing the location-to-field rule.
 */
export function fieldErrorsOf(error: unknown): FieldErrors {
  if (!isApiError(error) || error.status !== 422) return {};
  return fieldErrors({ errors: error.fieldErrors });
}

/**
 * The message that belongs to the form rather than to a field. When the server attributed the
 * failure to fields, those fields say so themselves and only the unattributed remainder is left
 * here — repeating "validation failed" above them adds noise, not information.
 */
export function formErrorOf(error: unknown): string | undefined {
  if (error === null || error === undefined) return undefined;
  const fields = fieldErrorsOf(error);
  if (Object.keys(fields).length > 0) return fields._;
  return errorDetail(error);
}
