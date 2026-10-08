import type { Actions } from "./$types";

export const actions = {
  default: async ({ request }) => {
    const form = await request.formData();
    return { echoed: String(form.get("message") ?? "") };
  },
} satisfies Actions;
