import { useParams } from "react-router-dom";
import { RepoList } from "./repos";
import { RepoView } from "./repoView";

export function GitPage() {
  const { repo } = useParams();
  return repo ? <RepoView name={repo} /> : <RepoList />;
}
