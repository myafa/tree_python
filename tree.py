class TreeNode:
    nodeLHS = ''
    nodeRHS = ''
    nodeParent = ''
    value = ''

    def __init__(self, value, nodeParent):
    	self.value = value
	self.nodeParent = nodeParent
    def getLHS(self):
  	return self.nodeLHS
    def getRHS(self):
	return self.nodeRHS
    def getValue(self):
	return self.value
    def setLHS(self,LHS):
	self.nodeLHS = LHS
    def setRHS(self,RHS):
	self.nodeRHS = RHS
    def setValue(self,value):
	self.value=value
    def setParent(self, nodeParent):
	self.nodeParent = nodeParent


