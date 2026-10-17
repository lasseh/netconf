# Text from a Junos device is put back together in the session

Junos keeps text as bytes and writes every byte above 127 as a separate
character reference. "å", loaded as the UTF-8 bytes C3 A5, comes back as
`&#195;&#165;` in every configuration format, in operational output
and in error messages, as captured from Junos 26.2. An XML parser reads
that as the two characters "Ã¥", so every value with a letter outside
ASCII was wrong for every caller.

For a device that advertises the Junos capability, the session normalizes
the replies and notifications it buffers. A run of references
whose bytes form one UTF-8 character becomes that character. `Reply.Raw`
is the normalized reply.

We preferred this to a fix in the `junos` package, on the way out of
`junos.GetConfiguration`, `junos.Decode`, `junos.Text` and `junos.JSON`.
That fix would keep the core free of vendor-specific code, but it would
leave the wrong text in the calls most programs make: `Session.Exec` into
a struct, `Reply.Element`, the message of a `netconf.RPCError`. A
`netconf.Element` is also a piece of the reply it was found in, so the
reply has to be right before anything is cut from it.

## Consequences

- `Reply.Raw` is not byte for byte what a Junos device sent. A reply read
  with `Session.Stream` is, and so is what `transport.Framer.DebugCapture`
  records.
- The core has vendor-specific behavior, and checks for the
  Junos capability to know when.
- A reference that is not part of a UTF-8 character stays as it is. Text
  that Junos holds in Latin-1 arrives as one reference per character,
  which a parser reads as that character, so it needs no help. When
  `&#229;` is loaded, Junos stores the byte E5 and sends `&#229;` back.
- Latin-1 text is misread where the bytes of two to four of its characters
  in a row form one UTF-8 character, as in "É»" or "ã¡¢". Junos does not
  say which encoding a text is in, so nothing tells the two apart.
- Only the form Junos writes for a byte is joined: `&#`, three decimal
  digits from 128 to 255, and `;`. Comments, CDATA sections and processing
  instructions are left alone, because a parser reads no references in
  them. The bytes of U+FFFE and U+FFFF are also left alone, because XML
  cannot hold them as characters.
- Normalization works in place, in the buffer the message was read into.
  A message without a numeric reference, `&#`, is not scanned at all.
- No notification from Junos has been looked at. Notifications are
  normalized because the session buffers them like replies, and
  normalization changes nothing in a message without such references.

## Update, 2026-10-17

The consequence "No notification from Junos has been looked at" records
the evidence at the time of the decision, not its current extent. Junos
notifications with text outside ASCII have since been captured and
tested. Integration runs read commit comments from notifications on
vJunos-router and vJunos-switch 26.2 with their characters intact. See
[What is verified against Junos](../junos-verified.md) for the platforms,
test scope and remaining limits. The decision is unchanged.
