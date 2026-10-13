import unittest
from analyze_cpu import subtract, union, window
from run_cpu import plan

class SchedulingTests(unittest.TestCase):
    def test_nested_interrupt_subtraction(self):
        blocks=union([(20,40),(25,30),(35,50),(70,90)])
        self.assertEqual(list(subtract(10,80,blocks,[b for a,b in blocks])),[(10,20),(50,70)])
    def test_migration_does_not_invent_parallelism(self):
        t=dict(intervals=[[0,500000,1,0],[500000,1000000,1,1]],ready=[])
        w=window(t,0,1000000,2,[3])
        self.assertEqual(w["maxSimultaneousLogical"],1)
        self.assertEqual(w["logicalTouched"],2)
        self.assertEqual(w["physicalTouched"],1)
        self.assertEqual(w["coreEquivalent"],1)
        self.assertEqual(w["twoOrMorePercent"],0)
    def test_real_overlap_and_empty_time(self):
        t=dict(intervals=[[100000,500000,1,0],[200000,400000,2,2]],ready=[])
        w=window(t,0,1000000,4,[3,12])
        self.assertAlmostEqual(w["coreEquivalent"],.6)
        self.assertEqual(w["maxSimultaneousLogical"],2)
        self.assertEqual(w["maxSimultaneousPhysical"],2)
        self.assertEqual(w["runningPercent"],{"0":60,"1":20,"2":20})
        self.assertEqual(w["peak100msCores"],2)
    def test_complete_predefined_plan(self):
        rows=plan()
        self.assertEqual(len(rows),3)
        for row in rows:
            self.assertEqual(len(row),15)
            self.assertEqual(len({a["id"] for a in row}),15)
            self.assertTrue(all(1<=a["n"]<=32 for a in row))
            self.assertEqual(sum(a["kind"]=="stepped" for a in row),1)

if __name__=="__main__":unittest.main()
