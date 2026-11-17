<script lang="ts">
  import Markdown from '../../shared/markdown/Markdown.svelte';
  import Card from '../../shared/ui/Card.svelte';
  import Section from '../../shared/ui/Section.svelte';
  import TextArea from '../../shared/ui/TextArea.svelte';

  const sample = `# Winter tyre quotes

Three garages answered. **Hillfield Motors** is the cheapest, *Ashby Tyres* fits on Saturday and ~~Fenwick~~ stopped answering.

Setext heading
--------------

1. Ask for the fitting slot
2. Confirm the price
   - with balancing
   - without disposal fee

- [ ] loose item with a paragraph

  and a second paragraph

> The quote holds until Friday.
> > A quote inside a quote.

| Garage | Price | Fitting |
| :--- | ---: | :---: |
| Hillfield Motors | 412.00 | Thursday |
| Ashby Tyres | 438.50 | Saturday |
| Fenwick | unknownunknownunknownunknownunknownunknownunknown | none |

\`\`\`json
{ "garage": "Hillfield Motors", "price": 412, "slot": "Thursday 09:00", "note": "averyveryverylongvaluewithoutanyspacesatallsothatitmustwrapinsidethecodeblock" }
\`\`\`

    indented code block

Inline \`code\`, a [same-app link](/inbox "Inbox"), an external https://example.com/quotes/winter-tyres-2026-with-a-long-path-that-wraps-on-a-phone, mail to <sam@example.test>, a reference [link][garage] and an image ![Map of the garage](https://example.com/map.png).

Entities: &copy; &amp; &#169; &#x1F697; and a hard  
break, then raw <b>HTML</b> shown as typed.

<div onclick="x()">A raw HTML block stays text.</div>

***

[garage]: https://example.com/hillfield "Hillfield Motors"
  `;

  let source = $state('');
</script>

<Section id="g-prose" data-gallery="prose" title="Markdown">
  <Card>
    <Markdown text={sample} />
  </Card>
  <TextArea label="Markdown source" rows={4} bind:value={source} />
  <div data-gallery="markdown-lab">
    <Markdown text={source} />
  </div>
</Section>
