import SimplifiedGraph from "../workspace/simplify/SimplifiedGraph";
import { sampleEdges, sampleNodes } from "../workspace/simplify/sampleData";

// Standalone playground for the simplified code-graph patterns:
//   semantic zooming · collapsible subgraphs · egocentric focus.
export default function GraphDemoPage() {
  return (
    <div className="h-full w-full bg-[#0a0a0a]">
      <SimplifiedGraph initialNodes={sampleNodes} initialEdges={sampleEdges} />
    </div>
  );
}
