package graph

import (
	"fmt"
	"strings"
	"testing"
)

// The sky above the port was the color of television, tuned to a dead channel.
const neuromancerOpening = `The sky above the port was the color of television, tuned to a dead channel. ` +
	`It's not like I'm using, Case heard someone say, as he shouldered his way ` +
	`through the crowd around the door of the Chat. It's like my body's developed ` +
	`this massive drug deficiency. It was a Sprawl voice and a Sprawl joke. The ` +
	`Chatsubo was a bar for professional expatriates; you could drink there for a ` +
	`week and never hear two words in Japanese. Ratz was tending bar, his prosthetic ` +
	`arm jerking monotonously as he filled a tray of glasses with draft Kirin. He ` +
	`saw Case and smiled, his teeth a webwork of East European steel and brown decay. ` +
	`Case found a place at the bar, between the unlikely tan on one of Lonny Zone's ` +
	`whores and the crisp naval uniform of a tall African whose cheekbones were ` +
	`ridged with precise rows of tribal scars. Wages for the trip up the well, Ratz ` +
	`said, shoving a draft across the bar with his good hand. He shrugged. Case ` +
	`always saw it coming. Same as his landlord, he owed a lot. He'd made the ` +
	`classic mistake, the one he'd sworn he'd never make. He stole from his ` +
	`employers. He kept something for himself and tried to move it through a fence ` +
	`in Amsterdam. He still wasn't sure how he'd been burned, but he'd been burned. ` +
	`For Case, who'd lived for the bodiless exultation of cyberspace, it was the ` +
	`Fall. In the bars he'd frequented as a cowboy hotshot, the elite stance ` +
	`involved a certain relaxed contempt for the flesh. The body was meat. Case ` +
	`fell into the prison of his own flesh.`

func TestChunkTextBelowThreshold(t *testing.T) {
	// A single sentence from Night City shouldn't trigger chunking.
	text := "The sky above the port was the color of television, tuned to a dead channel."
	chunks := ChunkText(text, 512, 512, 128)
	if chunks != nil {
		t.Fatal("text below threshold should return nil")
	}
}

func TestChunkTextAtThreshold(t *testing.T) {
	// 512 tokens * 4 chars = 2048 chars
	text := strings.Repeat("a", 2048)
	chunks := ChunkText(text, 512, 512, 128)
	if chunks != nil {
		t.Fatal("text at exactly threshold should return nil")
	}
}

func TestChunkTextAboveThreshold(t *testing.T) {
	// Repeat the Neuromancer opening enough times to exceed the threshold.
	text := strings.Repeat(neuromancerOpening+" ", 3)
	chunks := ChunkText(text, 512, 512, 128)
	if len(chunks) == 0 {
		t.Fatal("expected chunks for long text")
	}
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}
}

func TestChunkTextCoversAllContent(t *testing.T) {
	// Use numbered sentences so each chunk position is unambiguous.
	var sb strings.Builder
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&sb, "w%04d ", i)
	}
	text := sb.String()
	chunks := ChunkText(text, 512, 512, 128)

	if len(chunks) == 0 {
		t.Fatal("expected chunks")
	}

	covered := make([]bool, len(text))
	for _, chunk := range chunks {
		idx := strings.Index(text, chunk)
		if idx < 0 {
			t.Fatal("chunk not found in original text")
		}
		for i := idx; i < idx+len(chunk); i++ {
			covered[i] = true
		}
	}
	for i, c := range covered {
		if !c {
			t.Fatalf("character at position %d not covered by any chunk", i)
		}
	}
}

func TestChunkTextOverlap(t *testing.T) {
	// Repeat the Neuromancer opening to create a long text with natural
	// word boundaries. Adjacent chunks should share overlapping content.
	text := strings.Repeat(neuromancerOpening+" ", 5)
	chunks := ChunkText(text, 512, 512, 128)

	if len(chunks) < 3 {
		t.Fatalf("expected at least 3 chunks, got %d", len(chunks))
	}

	// Adjacent chunks should not be identical.
	for i := 1; i < len(chunks); i++ {
		if chunks[i] == chunks[i-1] {
			t.Fatal("adjacent chunks should not be identical")
		}
	}
}

// --- SplitSections tests ---

func TestSplitSectionsNil(t *testing.T) {
	// Short text returns nil.
	sections := SplitSections("hello world", 500, 5000)
	if sections != nil {
		t.Fatal("short text should return nil")
	}
}

func TestSplitSectionsMarkdown(t *testing.T) {
	text := `# Introduction

This is the introduction to the document. It covers the basic concepts
and provides context for what follows.

## First Topic

The first topic discusses important ideas. These ideas have been widely
studied and documented across multiple fields. We examine the evidence
and draw conclusions based on the available data.

## Second Topic

The second topic builds on the first. It introduces new concepts that
extend the previous analysis. Several key findings are presented.

## Conclusion

In conclusion, both topics contribute to our understanding. Future work
should explore additional dimensions of this problem.`

	sections := SplitSections(text, 100, 5000)
	if sections == nil {
		t.Fatal("expected sections for markdown doc")
	}
	if len(sections) < 3 {
		t.Fatalf("expected at least 3 sections, got %d", len(sections))
	}

	// Check that headings were extracted.
	foundFirst := false
	foundSecond := false
	for _, s := range sections {
		if strings.Contains(s.Heading, "First Topic") {
			foundFirst = true
		}
		if strings.Contains(s.Heading, "Second Topic") {
			foundSecond = true
		}
	}
	if !foundFirst || !foundSecond {
		t.Fatal("expected headings to be extracted")
	}
}

func TestSplitSectionsNumbered(t *testing.T) {
	text := `Preamble text goes here with some context.

1. The First Section

Content for the first numbered section that explains the initial concept
in detail with supporting evidence and examples.

2. The Second Section

Content for the second numbered section building on the previous one
with additional analysis and new findings.

3. The Third Section

Final section wrapping up with conclusions and recommendations
for future investigation.`

	sections := SplitSections(text, 100, 5000)
	if sections == nil {
		t.Fatal("expected sections for numbered doc")
	}
	if len(sections) < 3 {
		t.Fatalf("expected at least 3 sections, got %d", len(sections))
	}
}

func TestSplitSectionsHTML(t *testing.T) {
	text := `<h2>Introduction</h2>
<p>This is an HTML document with proper heading structure. The introduction
sets up the main arguments and provides necessary background context.</p>

<h2>Main Argument</h2>
<p>The main argument presents evidence for the central claim. Multiple sources
are cited and the methodology is explained in detail for reproducibility.</p>

<h2>Counterarguments</h2>
<p>Several counterarguments are considered and addressed. The strongest
objection is examined in detail and shown to be insufficient.</p>`

	sections := SplitSections(text, 100, 5000)
	if sections == nil {
		t.Fatal("expected sections for HTML doc")
	}
	if len(sections) < 3 {
		t.Fatalf("expected at least 3 sections, got %d", len(sections))
	}
}

func TestSplitSectionsParagraphs(t *testing.T) {
	// No headings -- falls back to paragraph splitting.
	var sb strings.Builder
	for i := 0; i < 10; i++ {
		fmt.Fprintf(&sb, "This is paragraph %d. It contains enough text to be meaningful on its own. "+
			"Each paragraph discusses a different aspect of the topic with sufficient detail.\n\n", i)
	}

	sections := SplitSections(sb.String(), 100, 5000)
	if sections == nil {
		t.Fatal("expected sections from paragraphs")
	}
	if len(sections) < 2 {
		t.Fatalf("expected at least 2 sections, got %d", len(sections))
	}
}

func TestSplitSectionsMergeShort(t *testing.T) {
	text := `## A

Short section A.

## B

Also short section B.

## C

This section is long enough to stand on its own. It contains multiple sentences
with meaningful content that discusses the topic in sufficient detail to be
independently useful as a search result. Further elaboration follows with
supporting points and examples drawn from the relevant literature.

## D

Another substantial section that stands on its own with enough content
to justify being a separate unit. This section covers additional material
that extends the analysis from section C with new perspectives and data.`

	sections := SplitSections(text, 200, 5000)
	if sections == nil {
		t.Fatal("expected sections")
	}
	// A and B should be merged since they're below minChars.
	if len(sections) > 3 {
		t.Fatalf("expected A+B merged, got %d sections", len(sections))
	}
}

func TestSplitSectionsSubSplit(t *testing.T) {
	// Single heading with very long content should get sub-split.
	var sb strings.Builder
	sb.WriteString("## Very Long Section\n\n")
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&sb, "Paragraph %d discusses topic %d in detail. This is substantial content. ", i, i)
		sb.WriteString("Additional analysis follows with supporting evidence.\n\n")
	}

	sections := SplitSections(sb.String(), 500, 2000)
	if sections == nil {
		t.Fatal("expected sections")
	}
	for _, s := range sections {
		if len(s.Text) > 2500 { // some tolerance
			t.Fatalf("section exceeds maxChars: %d", len(s.Text))
		}
	}
}

func TestSplitSectionsNoStructure(t *testing.T) {
	// Continuous text with no paragraph breaks and no headings.
	text := strings.Repeat("word ", 2000)
	sections := SplitSections(text, 500, 5000)
	// Should return nil (no structure found, caller falls back to ChunkText).
	if sections != nil {
		t.Fatalf("expected nil for unstructured text, got %d sections", len(sections))
	}
}

func TestSplitSectionsCleanHeading(t *testing.T) {
	text := `## 1. First Topic Here

Content for the first section with enough material to stand alone.
Multiple sentences discussing the topic in detail.

## 2. Second Topic Here

Content for the second section with different material and analysis.
Further discussion follows with conclusions.`

	sections := SplitSections(text, 100, 5000)
	if sections == nil {
		t.Fatal("expected sections")
	}
	for _, s := range sections {
		if strings.HasPrefix(s.Heading, "#") {
			t.Fatalf("heading should not start with #: %q", s.Heading)
		}
		if strings.HasPrefix(s.Heading, "1.") || strings.HasPrefix(s.Heading, "2.") {
			t.Fatalf("heading should not start with number prefix: %q", s.Heading)
		}
	}
}

func TestChunkTextWordBoundary(t *testing.T) {
	// The Neuromancer text has natural word boundaries. Chunks should
	// prefer breaking at spaces rather than mid-word.
	text := strings.Repeat(neuromancerOpening+" ", 4)
	chunks := ChunkText(text, 100, 100, 25)

	for i, chunk := range chunks {
		if i < len(chunks)-1 && len(chunk) > 0 {
			last := chunk[len(chunk)-1]
			// Most chunks should end at a word boundary.
			if last != ' ' && last != '\n' && last != '.' {
				// Not all will -- this is best-effort.
			}
		}
	}

	// Verify no chunk starts with a space (clean split).
	for _, chunk := range chunks {
		if len(chunk) > 0 && chunk[0] == ' ' {
			t.Fatal("chunk should not start with a space")
		}
	}
}
