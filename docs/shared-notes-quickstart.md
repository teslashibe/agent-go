# Create and open a shared group note

In an authorized two-person group, ask the agent to create a checklist. For
example: “Create a shared weekend list with towels, chargers, and snacks.”

The agent prepares access for the configured participants and includes an opening
link in its creation reply. Each participant opens that invitation using the Apple
Account associated with the invited phone number. Preparing access or copying a
link does not prove the invitation arrived or that either participant accepted it.

After opening the note, one participant can ask the agent to add an item and the
other can check it off in Notes. Both should observe the same checklist. For an
existing note, ask “Send me the link to our weekend list.” Link retrieval verifies
the configured participants; it never creates a replacement note or expands access.

The creation link is retained in the operation journal and included in the durable
reply even if the model omits it or a later checklist step returns a partial result.
If sharing itself is uncertain, keep the returned note ID and follow the documented
recovery process instead of creating or inviting again. A failed message delivery
remains subject to the normal outbox recovery rules.

This path supports the existing configured group participant set. Private Notes
are not implicitly shared. A shared flag alone does not establish authorization;
the native helper verifies the exact participants before returning an invitation
link. A person who was invited but has not opened the link is not a verified reader.

For release acceptance, use disposable notes and an unselected control. Verify the
first creation reply and both requested links on a recipient device, open the
correct note, then check an edit from each authorized participant. Record any
participant/device not exercised as unverified. See [verified behavior](verified.md)
for the current evidence boundary and [operations](operations.md) for recovery.

Apple documents invitation and account requirements in its
[shared Notes guide](https://support.apple.com/guide/notes/apd4e6e2c9a6/mac).
