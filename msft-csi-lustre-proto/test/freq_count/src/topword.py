class TopWord:
    def __init__(self, count, word):
        self.count = int(count)
        self.word = word
        return

    def __lt__(self, other):
        return self.count < other.count

    def outf(self):
        return self.word + " - appreared: " + str(self.count)