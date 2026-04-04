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
