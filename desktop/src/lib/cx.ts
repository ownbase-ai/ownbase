/** Joins the class names that survive, so a conditional can be written inline. */
export function cx(...parts: Array<string | false | null | undefined>): string {
  return parts.filter(Boolean).join(" ");
}
