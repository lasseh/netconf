# Text from a Junos device is put back together in the session

Junos keeps text as bytes and writes every byte above 127 as a character
reference of its own. "å" loaded as UTF-8 (the bytes C3 A5) comes back as
`&#195;&#165;` in every format of a configuration, in operational output
and in error messages (captured from Junos 26.2). An XML parser reads that
as the two characters "Ã¥", so every value with a letter outside ASCII was
wrong for every caller.

For a device that advertises the Junos capability the session rewrites the
messages it buffers, replies and notifications: references in a row whose
bytes are one character in UTF-8 become that character. `Reply.Raw` is the
rewritten reply.

We preferred this to a fix in the `junos` package, on the way out of
`GetConfiguration`, `Decode`, `Text` and `JSON`. That would keep the core
free of anything a vendor does, and leave the wrong text in the calls most
programs make: `Session.Exec` into a struct, `Reply.Element`, the message
of an `RPCError`. An `Element` is also a piece of the reply it was found
in, so the reply has to be right before anything is cut from it.

## Consequences

- `Reply.Raw` is not byte for byte what a Junos device sent. A reply read
  with `Session.Stream` is, and so is what the transport records with
  `DebugCapture`.
- The core does something on account of a vendor, and names the Junos
  capability to know when.
- A reference that is not part of a character in UTF-8 stays as it is.
  Text that Junos holds in Latin-1 arrives as one reference for each
  character, which is that character to a parser, and needs no help: Junos
  stores a `&#229;` that is loaded as the byte E5 and sends `&#229;` back.
- Latin-1 text is misread where two of its characters are UTF-8 as bytes,
  an upper-case letter with an accent followed by a sign from the upper
  half of Latin-1, as in "É»". Junos does not say which encoding a text is
  in, so there is nothing to tell the two by.
- Only what Junos writes for a byte is rewritten: `&#`, three decimal
  digits from 128 to 255 and `;`. Comments, CDATA sections and processing
  instructions are left alone, as a parser reads no references in them,
  and so are the bytes of U+FFFE and U+FFFF, which XML cannot hold as
  characters.
- The rewrite happens in the buffer the message was read into, and a
  message without a numeric reference is not gone through at all.
- No notification from Junos has been looked at. They are rewritten
  because the session buffers them like replies, and the rewrite does
  nothing to a message without such references.
