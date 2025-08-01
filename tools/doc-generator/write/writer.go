// SPDX-License-Identifier: AGPL-3.0-only
// Provenance-includes-location: https://github.com/cortexproject/cortex/blob/master/tools/doc-generator/writer.go
// Provenance-includes-license: Apache-2.0
// Provenance-includes-copyright: The Cortex Authors.

package write

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/grafana/regexp"
	"github.com/mitchellh/go-wordwrap"
	"gopkg.in/yaml.v3"

	"github.com/grafana/mimir/tools/doc-generator/parse"
)

const (
	maxLineWidth = 80
	tabWidth     = 2
)

type specWriter struct {
	out                strings.Builder
	modifyDescriptions func(string) string
}

func (w *specWriter) writeConfigBlock(b *parse.ConfigBlock, firstLineIndent, subsequentLineIndent int) {
	if len(b.Entries) == 0 {
		return
	}

	for i, entry := range b.Entries {
		if i > 0 {
			// Add a new line to separate from the previous entry, and reset indentation
			w.out.WriteString("\n")
			firstLineIndent = subsequentLineIndent
		}

		w.writeConfigEntry(entry, firstLineIndent, subsequentLineIndent)
	}
}

func (w *specWriter) writeConfigEntry(e *parse.ConfigEntry, firstLineIndent, subsequentLineIndent int) {
	switch e.Kind {
	case parse.KindBlock:
		// If the block is a root block it will have its dedicated section in the doc,
		// so here we've just to write down the reference without re-iterating on it.
		if e.Root {
			// Description
			w.writeComment(w.modifyDescriptions(e.BlockDesc), firstLineIndent, 0)
			w.writeExample(e.FieldExample, subsequentLineIndent)

			if e.Block.FlagsPrefix != "" {
				w.writeComment(fmt.Sprintf("The CLI flags prefix for this block configuration is: %s", e.Block.FlagsPrefix), subsequentLineIndent, 0)
			}

			// Block reference without entries, because it's a root block
			w.out.WriteString(pad(subsequentLineIndent) + "[" + e.Name + ": <" + e.Block.Name + ">]\n")
		} else {
			// Description
			w.writeComment(w.modifyDescriptions(e.BlockDesc), firstLineIndent, 0)
			w.writeExample(e.FieldExample, subsequentLineIndent)

			// Name
			w.out.WriteString(pad(subsequentLineIndent) + e.Name + ":\n")

			// Entries
			nextIndent := subsequentLineIndent + tabWidth
			w.writeConfigBlock(e.Block, nextIndent, nextIndent)
		}

	case parse.KindField:
		// Description
		w.writeComment(w.modifyDescriptions(e.Description()), firstLineIndent, 0)
		w.writeExample(e.FieldExample, subsequentLineIndent)
		w.writeFlag(e.FieldFlag, subsequentLineIndent)

		// Specification
		fieldDefault := e.FieldDefault
		switch e.FieldType {
		case "string":
			fieldDefault = strconv.Quote(fieldDefault)
		case "duration":
			fieldDefault = cleanupDuration(fieldDefault)
		}

		if e.Required {
			w.out.WriteString(pad(subsequentLineIndent) + e.Name + ": <" + e.FieldType + "> | default = " + fieldDefault + "\n")
		} else {
			w.out.WriteString(pad(subsequentLineIndent) + "[" + e.Name + ": <" + e.FieldType + "> | default = " + fieldDefault + "]\n")
		}

	case parse.KindMap:
		// Description
		w.writeComment(w.modifyDescriptions(e.Description()), firstLineIndent, 0)
		w.writeExample(e.FieldExample, subsequentLineIndent)
		w.writeFlag(e.FieldFlag, subsequentLineIndent)

		// Specification
		if e.Required {
			w.out.WriteString(pad(subsequentLineIndent) + e.Name + ":\n")
		} else {
			w.out.WriteString(pad(subsequentLineIndent) + "[" + e.Name + ":]\n")
		}

		w.out.WriteString(pad(subsequentLineIndent+tabWidth) + "<" + e.FieldType + ">:\n")
		nextIndent := subsequentLineIndent + (2 * tabWidth)
		w.writeConfigBlock(e.Element, nextIndent, nextIndent)

	case parse.KindSlice:
		// Description
		w.writeComment(w.modifyDescriptions(e.Description()), firstLineIndent, 0)
		w.writeExample(e.FieldExample, subsequentLineIndent)

		// Name
		w.out.WriteString(pad(subsequentLineIndent) + e.Name + ":\n")

		// Element
		w.out.WriteString(pad(subsequentLineIndent+tabWidth) + "- ") // Don't add a trailing new line: we want the first line of the first element to appear directly after the "-"
		w.writeConfigBlock(e.Element, 0, subsequentLineIndent+(2*tabWidth))
	}
}

func (w *specWriter) writeFlag(name string, indent int) {
	if name == "" {
		return
	}

	w.out.WriteString(pad(indent) + "# CLI flag: -" + name + "\n")
}

func (w *specWriter) writeComment(comment string, indent, innerIndent int) {
	if comment == "" {
		return
	}

	wrapped := wordwrap.WrapString(comment, uint(maxLineWidth-indent-innerIndent-2))
	w.writeWrappedString(wrapped, indent, innerIndent)
}

func (w *specWriter) writeExample(example *parse.FieldExample, indent int) {
	if example == nil {
		return
	}

	w.writeComment("Example:", indent, 0)
	if example.Comment != "" {
		w.writeComment(w.modifyDescriptions(example.Comment), indent, 2)
	}

	data, err := yaml.Marshal(example.Yaml)
	if err != nil {
		panic(fmt.Errorf("can't render example: %w", err))
	}

	w.writeWrappedString(string(data), indent, 2)
}

func (w *specWriter) writeWrappedString(s string, indent, innerIndent int) {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for _, line := range lines {
		w.out.WriteString(pad(indent) + "# " + pad(innerIndent) + line + "\n")
	}
}

func (w *specWriter) string() string {
	return strings.TrimSpace(w.out.String())
}

type MarkdownWriter struct {
	out                strings.Builder
	ModifyDescriptions func(string) string
}

func (w *MarkdownWriter) WriteConfigDoc(blocks []*parse.ConfigBlock, rootBlocks []parse.RootBlock) {
	// Deduplicate root blocks.
	uniqueBlocks := map[string]*parse.ConfigBlock{}
	for _, block := range blocks {
		uniqueBlocks[block.Name] = block
	}

	// Generate the markdown, honoring the root blocks order.
	if topBlock, ok := uniqueBlocks[""]; ok {
		w.WriteConfigBlock(topBlock)
	}

	for _, rootBlock := range rootBlocks {
		if block, ok := uniqueBlocks[rootBlock.Name]; ok {
			// Keep the root block description.
			blockToWrite := *block
			blockToWrite.Desc = rootBlock.Desc

			w.WriteConfigBlock(&blockToWrite)
		}
	}
}

func (w *MarkdownWriter) WriteConfigBlock(block *parse.ConfigBlock) {
	// Title
	if block.Name != "" {
		w.out.WriteString("### " + block.Name + "\n")
		w.out.WriteString("\n")
	}

	// Description
	if block.Desc != "" {
		desc := block.Desc

		// Wrap first instance of the config block name with backticks
		if block.Name != "" {
			var matches int
			nameRegexp := regexp.MustCompile(regexp.QuoteMeta(block.Name))
			desc = nameRegexp.ReplaceAllStringFunc(desc, func(input string) string {
				if matches == 0 {
					matches++
					return "`" + input + "`"
				}
				return input
			})
		}

		// List of all prefixes used to reference this config block.
		if len(block.FlagsPrefixes) > 1 {
			sortedPrefixes := sort.StringSlice(block.FlagsPrefixes)
			sortedPrefixes.Sort()

			desc += " The supported CLI flags `<prefix>` used to reference this configuration block are:\n\n"

			for _, prefix := range sortedPrefixes {
				if prefix == "" {
					desc += "- _no prefix_\n"
				} else {
					desc += fmt.Sprintf("- `%s`\n", prefix)
				}
			}

			// Unfortunately the markdown compiler used by the website generator has a bug
			// when there's a list followed by a code block (no matter know many newlines
			// in between). To workaround it we add a non-breaking space.
			desc += "\n&nbsp;"
		}

		w.out.WriteString(desc + "\n")
		w.out.WriteString("\n")
	}

	// Config specs
	spec := &specWriter{
		modifyDescriptions: w.ModifyDescriptions,
	}

	if w.ModifyDescriptions == nil {
		spec.modifyDescriptions = func(s string) string { return s }
	}

	spec.writeConfigBlock(block, 0, 0)

	w.out.WriteString("```yaml\n")
	w.out.WriteString(spec.string() + "\n")
	w.out.WriteString("```\n")
	w.out.WriteString("\n")
}

func (w *MarkdownWriter) String() string {
	return strings.TrimSpace(w.out.String())
}

func pad(length int) string {
	return strings.Repeat(" ", length)
}

func cleanupDuration(value string) string {
	// This is the list of suffixes to remove from the duration if they're not
	// the whole duration value.
	suffixes := []string{"0s", "0m"}

	for _, suffix := range suffixes {
		re := regexp.MustCompile("(^.+\\D)" + suffix + "$")

		if groups := re.FindStringSubmatch(value); len(groups) == 2 {
			value = groups[1]
		}
	}

	return value
}
