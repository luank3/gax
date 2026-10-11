package tools

import (
	"bytes"
	"fmt"
	"strings"
)

const (
	diffContextLines = 3
	diffWindowSlack  = 64
	diffRegionMin    = 64
	diffRegionMax    = 1024
	diffWorkLimit    = 1 << 28
)

type diffSide struct {
	data    []byte
	offsets []uint32
	hashes  []uint32
}

func newDiffSide(data []byte) diffSide {
	lineCount := bytes.Count(data, []byte{'\n'})
	if len(data) > 0 && data[len(data)-1] != '\n' {
		lineCount++
	}
	side := diffSide{data: data, offsets: make([]uint32, lineCount+1), hashes: make([]uint32, lineCount)}
	line := 0
	for index, value := range data {
		if value == '\n' {
			line++
			side.offsets[line] = uint32(index + 1)
		}
	}
	if line < lineCount {
		side.offsets[lineCount] = uint32(len(data))
	}
	for index := range side.hashes {
		hash := uint32(2166136261)
		for _, value := range data[side.offsets[index]:side.offsets[index+1]] {
			hash ^= uint32(value)
			hash *= 16777619
		}
		side.hashes[index] = hash
	}
	return side
}

func (side diffSide) line(index int) []byte {
	return side.data[side.offsets[index]:side.offsets[index+1]]
}

func (side diffSide) equal(left int, other diffSide, right int) bool {
	if side.hashes[left] != other.hashes[right] {
		return false
	}
	return bytes.Equal(side.line(left), other.line(right))
}

func (side diffSide) blank(index int) bool {
	for _, value := range side.line(index) {
		if value != ' ' && value != '\t' && value != '\r' && value != '\n' {
			return false
		}
	}
	return true
}

type diffWindow struct {
	lo, oldHi, newHi, base int
}

func findDiffWindow(oldData, newData []byte) diffWindow {
	common := min(len(oldData), len(newData))
	prefix := 0
	for prefix < common && oldData[prefix] == newData[prefix] {
		prefix++
	}
	lo := prefix
	for lo > 0 && oldData[lo-1] != '\n' {
		lo--
	}
	for index := 0; index < diffWindowSlack && lo > 0; index++ {
		lo--
		for lo > 0 && oldData[lo-1] != '\n' {
			lo--
		}
	}

	window := diffWindow{lo: lo, base: bytes.Count(oldData[:lo], []byte{'\n'})}
	suffix := 0
	suffixMax := common - lo
	for suffix < suffixMax && oldData[len(oldData)-1-suffix] == newData[len(newData)-1-suffix] {
		suffix++
	}
	if suffix > 0 {
		start := len(oldData) - suffix
		newline := bytes.IndexByte(oldData[start:start+suffix], '\n')
		if newline >= 0 {
			hi := start + newline + 1
			for index := 0; index < diffWindowSlack && hi < len(oldData); index++ {
				next := bytes.IndexByte(oldData[hi:], '\n')
				if next < 0 {
					hi = len(oldData)
				} else {
					hi += next + 1
				}
			}
			window.oldHi = hi
			window.newHi = len(newData) - (len(oldData) - hi)
			return window
		}
	}
	window.oldHi, window.newHi = len(oldData), len(newData)
	return window
}

type diffContext struct {
	oldSide, newSide diffSide
	oldChanged       []bool
	newChanged       []bool
	forward          []int
	backward         []int
	vectorOffset     int
	work             int
}

func (context *diffContext) equal(oldLine, newLine int) bool {
	if context.work == 0 {
		return false
	}
	context.work--
	return context.oldSide.equal(oldLine, context.newSide, newLine)
}

func (context *diffContext) equalReverse(oldHi, newHi, x, y int) bool {
	return context.equal(oldHi-1-x, newHi-1-y)
}

func splitPoint(oldLo, newLo, n, m, x, y int) (int, int) {
	return oldLo + max(0, min(n, x)), newLo + max(0, min(m, y))
}

func (context *diffContext) myersSplit(oldLo, oldHi, newLo, newHi int) (int, int) {
	n, m := oldHi-oldLo, newHi-newLo
	delta := n - m
	odd := delta%2 != 0
	offset := context.vectorOffset
	forward, backward := context.forward, context.backward

	budget := diffRegionMin
	for budget < diffRegionMax && budget*budget < n+m {
		budget *= 2
	}
	if budget > (n+m)/2+1 {
		budget = (n+m)/2 + 1
	}
	best, bestX, bestY := -1, 0, 0
	forward[offset+1], backward[offset+1] = 0, 0

	for distance := 0; distance <= budget && context.work > 0; distance++ {
		for diagonal := -distance; diagonal <= distance; diagonal += 2 {
			index := offset + diagonal
			var x int
			if diagonal == -distance || diagonal != distance && forward[index-1] < forward[index+1] {
				x = forward[index+1]
			} else {
				x = forward[index-1] + 1
			}
			y := x - diagonal
			for x < n && y < m && context.equal(oldLo+x, newLo+y) {
				x++
				y++
			}
			forward[index] = x
			if x <= n && y <= m && x+y > best {
				best, bestX, bestY = x+y, x, y
			}
			reverseDiagonal := delta - diagonal
			if odd && reverseDiagonal >= -(distance-1) && reverseDiagonal <= distance-1 && x >= n-backward[offset+reverseDiagonal] {
				return splitPoint(oldLo, newLo, n, m, x, y)
			}
		}
		for diagonal := -distance; diagonal <= distance; diagonal += 2 {
			index := offset + diagonal
			var x int
			if diagonal == -distance || diagonal != distance && backward[index-1] < backward[index+1] {
				x = backward[index+1]
			} else {
				x = backward[index-1] + 1
			}
			y := x - diagonal
			for x < n && y < m && context.equalReverse(oldHi, newHi, x, y) {
				x++
				y++
			}
			backward[index] = x
			forwardDiagonal := delta - diagonal
			if !odd && forwardDiagonal >= -distance && forwardDiagonal <= distance && forward[offset+forwardDiagonal] >= n-x {
				return splitPoint(oldLo, newLo, n, m, n-x, m-y)
			}
		}
	}
	return splitPoint(oldLo, newLo, n, m, bestX, bestY)
}

func (context *diffContext) diffRegion(oldLo, oldHi, newLo, newHi int) {
	for {
		for oldLo < oldHi && newLo < newHi && context.equal(oldLo, newLo) {
			oldLo++
			newLo++
		}
		for oldHi > oldLo && newHi > newLo && context.equal(oldHi-1, newHi-1) {
			oldHi--
			newHi--
		}
		if oldLo == oldHi {
			for index := newLo; index < newHi; index++ {
				context.newChanged[index] = true
			}
			return
		}
		if newLo == newHi {
			for index := oldLo; index < oldHi; index++ {
				context.oldChanged[index] = true
			}
			return
		}

		splitOld, splitNew := oldLo, newLo
		degenerate := true
		if context.work > 0 {
			splitOld, splitNew = context.myersSplit(oldLo, oldHi, newLo, newHi)
			degenerate = splitOld == oldLo && splitNew == newLo || splitOld == oldHi && splitNew == newHi
		}
		if degenerate {
			for index := oldLo; index < oldHi; index++ {
				context.oldChanged[index] = true
			}
			for index := newLo; index < newHi; index++ {
				context.newChanged[index] = true
			}
			return
		}

		leftSize := splitOld - oldLo + splitNew - newLo
		rightSize := oldHi - splitOld + newHi - splitNew
		if leftSize <= rightSize {
			context.diffRegion(oldLo, splitOld, newLo, splitNew)
			oldLo, newLo = splitOld, splitNew
		} else {
			context.diffRegion(splitOld, oldHi, splitNew, newHi)
			oldHi, newHi = splitOld, splitNew
		}
	}
}

func (context *diffContext) slideRuns(side diffSide, changed []bool) {
	for scan := 0; scan < len(changed); {
		if !changed[scan] {
			scan++
			continue
		}
		start, end := scan, scan
		for end < len(changed) && changed[end] {
			end++
		}
		run := end - start
		lo := start
		for lo > 0 && !changed[lo-1] && side.equal(lo-1, side, lo+run-1) {
			lo--
		}
		hi := start
		for hi+run < len(changed) && !changed[hi+run] && side.equal(hi, side, hi+run) {
			hi++
		}
		best, bestScore := lo, -1
		for position := lo; position <= hi; position++ {
			score := 0
			if position == 0 || side.blank(position-1) {
				score += 2
			}
			if position+run == len(changed) || side.blank(position+run-1) {
				score++
			}
			if score >= bestScore {
				best, bestScore = position, score
			}
		}
		if best != start {
			clear(changed[start : start+run])
			for index := best; index < best+run; index++ {
				changed[index] = true
			}
		}
		scan = max(best, start) + run
	}
}

type diffChange struct {
	oldStart, oldLines int
	newStart, newLines int
}

func collectDiffChanges(oldChanged, newChanged []bool) []diffChange {
	changes := make([]diffChange, 0)
	oldLine, newLine := 0, 0
	for oldLine < len(oldChanged) || newLine < len(newChanged) {
		if oldLine < len(oldChanged) && oldChanged[oldLine] || newLine < len(newChanged) && newChanged[newLine] {
			change := diffChange{oldStart: oldLine, newStart: newLine}
			for oldLine < len(oldChanged) && oldChanged[oldLine] {
				oldLine++
			}
			for newLine < len(newChanged) && newChanged[newLine] {
				newLine++
			}
			change.oldLines, change.newLines = oldLine-change.oldStart, newLine-change.newStart
			changes = append(changes, change)
		} else {
			oldLine++
			newLine++
		}
	}
	return changes
}

func appendDiffLine(output *strings.Builder, marker byte, side diffSide, line int) {
	output.WriteByte(marker)
	text := side.line(line)
	output.Write(text)
	if text[len(text)-1] != '\n' {
		output.WriteString("\n\\ No newline at end of file\n")
	}
}

func appendDiffRange(output *strings.Builder, start, count int) {
	if count == 1 {
		fmt.Fprintf(output, "%d", start+1)
		return
	}
	if count == 0 {
		fmt.Fprintf(output, "%d,0", start)
		return
	}
	fmt.Fprintf(output, "%d,%d", start+1, count)
}

func appendDiffHunks(output *strings.Builder, oldSide, newSide diffSide, changes []diffChange, base int) {
	for group := 0; group < len(changes); {
		last := group
		for last+1 < len(changes) && changes[last+1].oldStart-(changes[last].oldStart+changes[last].oldLines) <= 2*diffContextLines {
			last++
		}
		oldEnd := changes[last].oldStart + changes[last].oldLines
		newEnd := changes[last].newStart + changes[last].newLines
		oldLo := max(0, changes[group].oldStart-diffContextLines)
		oldHi := min(oldEnd+diffContextLines, len(oldSide.hashes))
		newLo := changes[group].newStart - (changes[group].oldStart - oldLo)
		newHi := newEnd + (oldHi - oldEnd)
		output.WriteString("@@ -")
		appendDiffRange(output, base+oldLo, oldHi-oldLo)
		output.WriteString(" +")
		appendDiffRange(output, base+newLo, newHi-newLo)
		output.WriteString(" @@\n")

		oldLine := oldLo
		for changeIndex := group; changeIndex <= last; changeIndex++ {
			change := changes[changeIndex]
			for ; oldLine < change.oldStart; oldLine++ {
				appendDiffLine(output, ' ', oldSide, oldLine)
			}
			for ; oldLine < change.oldStart+change.oldLines; oldLine++ {
				appendDiffLine(output, '-', oldSide, oldLine)
			}
			for newLine := change.newStart; newLine < change.newStart+change.newLines; newLine++ {
				appendDiffLine(output, '+', newSide, newLine)
			}
		}
		for ; oldLine < oldHi; oldLine++ {
			appendDiffLine(output, ' ', oldSide, oldLine)
		}
		group = last + 1
	}
}

func unifiedDiff(oldData, newData []byte, oldLabel, newLabel string) string {
	if bytes.Equal(oldData, newData) {
		return ""
	}
	window := findDiffWindow(oldData, newData)
	oldWindow := oldData[window.lo:window.oldHi]
	newWindow := newData[window.lo:window.newHi]
	var output strings.Builder
	output.Grow(len(oldWindow) / 8)
	fmt.Fprintf(&output, "--- %s\n+++ %s\n", oldLabel, newLabel)
	if uint64(len(oldWindow)) > uint64(^uint32(0)) || uint64(len(newWindow)) > uint64(^uint32(0)) {
		oldCount := countDiffLines(oldWindow)
		newCount := countDiffLines(newWindow)
		output.WriteString("@@ -")
		appendDiffRange(&output, window.base, oldCount)
		output.WriteString(" +")
		appendDiffRange(&output, window.base, newCount)
		output.WriteString(" @@\n")
		appendDiffRegionLines(&output, '-', oldWindow)
		appendDiffRegionLines(&output, '+', newWindow)
	} else {
		oldSide, newSide := newDiffSide(oldWindow), newDiffSide(newWindow)
		context := diffContext{
			oldSide: oldSide, newSide: newSide,
			oldChanged: make([]bool, len(oldSide.hashes)), newChanged: make([]bool, len(newSide.hashes)),
			forward: make([]int, 2*(2*diffRegionMax+3)), work: diffWorkLimit,
		}
		context.backward = context.forward[2*diffRegionMax+3:]
		context.vectorOffset = diffRegionMax + 1
		context.diffRegion(0, len(oldSide.hashes), 0, len(newSide.hashes))
		context.slideRuns(oldSide, context.oldChanged)
		context.slideRuns(newSide, context.newChanged)
		appendDiffHunks(&output, oldSide, newSide, collectDiffChanges(context.oldChanged, context.newChanged), window.base)
	}
	clean := strings.ReplaceAll(output.String(), "\x00", "\uFFFD")
	return strings.ToValidUTF8(clean, "\uFFFD")
}

func countDiffLines(data []byte) int {
	count := bytes.Count(data, []byte{'\n'})
	if len(data) > 0 && data[len(data)-1] != '\n' {
		count++
	}
	return count
}

func appendDiffRegionLines(output *strings.Builder, marker byte, data []byte) {
	for start := 0; start < len(data); {
		end := start + bytes.IndexByte(data[start:], '\n') + 1
		if end <= start {
			end = len(data)
		}
		output.WriteByte(marker)
		output.Write(data[start:end])
		if data[end-1] != '\n' {
			output.WriteString("\n\\ No newline at end of file\n")
		}
		start = end
	}
}
