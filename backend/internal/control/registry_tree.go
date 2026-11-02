package control

// NewScratchTree is a Tree over files that belongs to no edit: nothing it holds is validated or
// archived. Admin handlers use it to normalise one document with SetScalar/SetString before
// putting it into the tree of a real Edit.
func NewScratchTree(files map[string][]byte) Tree {
	if files == nil {
		files = map[string][]byte{}
	}
	return Tree{&tree{files: files}}
}
