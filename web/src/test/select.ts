import { screen, within } from '@testing-library/react'
import type userEvent from '@testing-library/user-event'

// choose picks an option of a Select (Radix) found by its label, inside scope
// (a dialog, say) when given.
export async function choose(user: ReturnType<typeof userEvent.setup>, label: string | RegExp, option: string | RegExp, scope?: HTMLElement) {
  await user.click((scope ? within(scope) : screen).getByLabelText(label))
  await user.click(await screen.findByRole('option', { name: option }))
}
