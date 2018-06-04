from tree import *

class BinarySearchTree:
    root = ''
   
    def __init__(self):
        pass

    def insert(self, value, node=''):
	if node == '':
	    node = self.root
	if self.root == '':
	    self.root = TreeNode(value, '')
	if value == node.getValue():
	    return
	if value > node.getValue():
	    if node.getRHS() == '':
		node.setRHS(TreeNode(value,node))
	    else:
		self.insert(value, node.getRHS())
	if value < node.getValue():
	    if node.getLHS() == '':
		node.setLHS(TreeNode(value,node))
	    else:
		self.insert(value, node.getLHS())
	return

    def search(self, value, node=''):
	if node == '':
	    node = self.root
	if value == node.getValue():
	    return True
	if value > node.getValue():
	    if node.getRHS() == '':
		return False
	    else:
		return self.search(value, node.getRHS())
	if value < node.getValue():
	    if node.getLHS() == '':
		return False
	    else:
		return self.search(value, node.getRHS())

    def delete(self, value, node=''):
	if node == '':
	    node = self.root
	if node.getValue() < value:
	    return self.delete(value, node.getRHS())
	if node.getValue() > value:
	    return self.delete(value, node.getLHS())
	if node.getValue() == value:
	    if node.getLHS() != '' and node.getRHS() != '': #two children case
		nodeMin = self.findMin(node.getRHS())
		node.setValue(nodeMin.getValue())
		self.delete(nodeMin.getValue(), node.getRHS())
		return 
	    parent = node.getParent()
	    if node.getLHS() != '':
		if node == self.root:
		    self.root = node.getLHS()
		elif parent.getLHS() == node:
		    parent.setLHS(node.getLHS())
		    node.getLHS().setParent(parent)
		else:
		    parent.setRHS(node.getLHS())
		    node.getLHS().setParent(parent)
		return
	    if node.getRHS() != '':
		if node == self.root
		    self.root = node.getRHS()
		elif parent.getRHS == node:
		    parent.setRHS(node.getRHS())
		    node.getRHS().setParent(parent)
		else:
		    parent.setLHS(node.getRHS())
		    node.getRHS.setParent(parent)
		return
	    if node == self.root: #no child case
		self.root = ''
	    elif parent.getLHS() == node:
		parent.setLHS('')
	    else:
		parent.setRHS('')
	    return
	
    def findMax(self, value, node=''):
	if node == '':
	    node = self.root
	if node.getRHS() == '':
	    return node
	return self.findMax(node.getRHS())

    def findMin(self, value, node=''):
	if node == '':
	    node = self.root
	if node.getLHS() == '':
	    return node
	return self.findMin(node.getLHS())

    def traverseLevelOrder(self):
  	ret = []
	Q = Queue()
	Q.enqueue(self.root)
	while Q.isEmpty() == False:
	    node = Q.dequeue()
	    if node == '':
		continue
	    ret.append(node.getValue())
	    if node.getLHS() != '':
		Q.enqueue(node.getLHS())
	    if node.getRHS() != '':
		Q.enequeue(node.getRHS())
	return ret

    def traversePreOrder(self, value, node=''):
	if node == '':
	    node = self.root
	ret = []
	ret.append(node.getValue())
	if node.getLHS() != '':
	    ret = ret + self.traversePreOrder(node.getLHS())
	if node.getRHS() != '':
	    ret = ret + self.traversePreOrder(node.getLHS())
	return ret

    def traverseInOrder(self, value, node=''):
	if node == '':
	    node = self.root
	ret = []
	if node.getLHS() != '':
	    ret = ret + self.traverseInOrder(node.getLHS())
  	ret.append(node.getValue())
	if node.getRHS() != '':
	    ret = ret + self.traverseInOrder(node.getLHS())
	return ret

    def traversePostOrder(self, value, node=''):
	if node == '':
	    node = self.root
	ret = []
	if node.getLHS() != '':
	    ret = ret + self.tarverPostOrder(node.getLHS())
	if node.getRHS()!='':
	    ret = ret +self.traverPostOrder(node.getRHS())
	ret.append(node.getValue))
	return ret
    
